import { Logger } from "@aws-lambda-powertools/logger";
import { SendDurableExecutionCallbackSuccessCommand } from "@aws-sdk/client-lambda";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeHandler, REVIEW_DECIDED } from "../src/handler";

const logger = new Logger({ serviceName: "test", logLevel: "SILENT" });

function decided(data: Record<string, unknown>, type = REVIEW_DECIDED) {
  return { specversion: "1.0", id: "ce-1", type, source: "urn:demo:cli", subject: "npm/left-pad", datacontenttype: "application/json", data };
}

const approve = { callbackId: "cb-1", decision: "approve", reason: "looked fine", reviewer: "alice" };

function named(name: string) {
  return Object.assign(new Error(name), { name });
}

describe("dispatcher", () => {
  it("completes the callback with the decision", async () => {
    const send = vi.fn().mockResolvedValue({});
    await makeHandler({ send }, logger)(decided(approve));
    expect(send).toHaveBeenCalledOnce();
    const cmd = send.mock.calls[0][0] as SendDurableExecutionCallbackSuccessCommand;
    expect(cmd).toBeInstanceOf(SendDurableExecutionCallbackSuccessCommand);
    expect(cmd.input.CallbackId).toBe("cb-1");
    // Result is typed as BlobPayloadInputTypes (string | Uint8Array | ...); we always encode a Uint8Array.
    expect(JSON.parse(new TextDecoder().decode(cmd.input.Result as Uint8Array))).toEqual(approve);
  });

  it("accepts an array and JSON text", async () => {
    const send = vi.fn().mockResolvedValue({});
    await makeHandler({ send }, logger)([decided(approve), JSON.stringify(decided({ ...approve, callbackId: "cb-2" }))]);
    expect(send).toHaveBeenCalledTimes(2);
  });

  it.each([
    ["an unknown decision", decided({ ...approve, decision: "maybe" })],
    ["a missing callbackId", decided({ decision: "approve" })],
    ["another event type", decided(approve, "dev.example.package.verdict")],
    ["a non-CloudEvent", { Data: {}, Metadata: {} }],
    ["unparseable text", "{not json"],
  ])("drops %s without calling Lambda", async (_name, payload) => {
    const send = vi.fn();
    await expect(makeHandler({ send }, logger)(payload)).resolves.toBeUndefined();
    expect(send).not.toHaveBeenCalled();
  });

  it.each(["ResourceNotFoundException", "InvalidParameterValueException", "CallbackTimeoutException"])(
    "drops a decision for a closed callback (%s)",
    async (name) => {
      const send = vi.fn().mockRejectedValue(named(name));
      await expect(makeHandler({ send }, logger)(decided(approve))).resolves.toBeUndefined();
    },
  );

  it("rethrows transient errors so EventBridge retries", async () => {
    const send = vi.fn().mockRejectedValue(named("TooManyRequestsException"));
    await expect(makeHandler({ send }, logger)(decided(approve))).rejects.toThrow("TooManyRequestsException");
  });

  describe("logging", () => {
    afterEach(() => {
      vi.restoreAllMocks();
    });

    it("emits log lines carrying component=dispatcher", async () => {
      // Powertools writes via its own Console bound to process.stdout, not the global console.
      const writeSpy = vi.spyOn(process.stdout, "write").mockImplementation(() => true);
      const componentLogger = new Logger({ serviceName: "dispatcher", persistentKeys: { component: "dispatcher" } });
      const send = vi.fn().mockResolvedValue({});

      await makeHandler({ send }, componentLogger)(decided(approve));

      expect(writeSpy).toHaveBeenCalled();
      const lines = writeSpy.mock.calls.map(([line]) => JSON.parse(line as string));
      expect(lines.some((line) => line.component === "dispatcher")).toBe(true);
    });
  });
});
