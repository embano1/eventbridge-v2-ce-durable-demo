import { Logger } from "@aws-lambda-powertools/logger";
import { LambdaClient, SendDurableExecutionCallbackSuccessCommand } from "@aws-sdk/client-lambda";

export const REVIEW_DECIDED = "dev.example.review.decided";

// Retrying cannot fix these: the callback is unknown, already completed, or expired.
const FINAL_ERRORS = new Set(["ResourceNotFoundException", "InvalidParameterValueException", "CallbackTimeoutException"]);

export interface ReviewDecided {
  callbackId: string;
  decision: "approve" | "reject";
  reason: string;
  reviewer: string;
}

interface CloudEvent {
  specversion: string;
  id: string;
  type: string;
  source: string;
  subject?: string;
  data?: unknown;
}

function parseDecision(raw: unknown): { ce: CloudEvent; decision: ReviewDecided } {
  const value: unknown = typeof raw === "string" ? JSON.parse(raw) : raw;
  if (!value || typeof value !== "object") throw new Error("payload is not an object");
  const ce = value as CloudEvent;
  if (ce.specversion !== "1.0" || !ce.id || !ce.type) throw new Error("not a structured CloudEvent");
  if (ce.type !== REVIEW_DECIDED) throw new Error(`unexpected type ${ce.type}`);
  const d = (ce.data ?? {}) as Partial<ReviewDecided>;
  if (!d.callbackId) throw new Error("data.callbackId is missing");
  if (d.decision !== "approve" && d.decision !== "reject") throw new Error(`invalid decision ${String(d.decision)}`);
  return { ce, decision: { callbackId: d.callbackId, decision: d.decision, reason: d.reason ?? "", reviewer: d.reviewer ?? "" } };
}

type Sender = Pick<LambdaClient, "send">;

export function makeHandler(client: Sender, logger: Logger) {
  return async (payload: unknown): Promise<void> => {
    for (const raw of Array.isArray(payload) ? payload : [payload]) {
      let parsed: ReturnType<typeof parseDecision>;
      try {
        parsed = parseDecision(raw);
      } catch (error) {
        logger.error("dropping invalid event", { error: error as Error, payload: raw });
        continue;
      }
      const { ce, decision } = parsed;
      const keys = { ce_id: ce.id, ce_type: ce.type, ce_source: ce.source, ce_subject: ce.subject, callback_id: decision.callbackId, decision: decision.decision };
      try {
        await client.send(
          new SendDurableExecutionCallbackSuccessCommand({
            CallbackId: decision.callbackId,
            Result: new TextEncoder().encode(JSON.stringify(decision)),
          }),
        );
        logger.info("callback completed", keys);
      } catch (error) {
        if (error instanceof Error && FINAL_ERRORS.has(error.name)) {
          logger.error("callback is closed or unknown, dropping decision", { ...keys, error });
          continue;
        }
        logger.error("callback failed, EventBridge will retry", { ...keys, error: error as Error });
        throw error;
      }
    }
  };
}

export const handler = makeHandler(
  new LambdaClient({}),
  new Logger({ serviceName: "dispatcher", persistentKeys: { component: "dispatcher" } }),
);
