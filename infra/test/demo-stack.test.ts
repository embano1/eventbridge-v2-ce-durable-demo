import { App } from "aws-cdk-lib";
import { Match, Template } from "aws-cdk-lib/assertions";
import { describe, expect, it } from "vitest";
import { DemoStack, EXECUTION_NAME, TYPES } from "../lib/demo-stack";

const t = Template.fromStack(new DemoStack(new App(), "Test", { env: { account: "111122223333", region: "eu-central-1" } }));
const subscribers = t.findResources("AWS::EventsV2::Subscriber");

describe("DemoStack", () => {
  it("creates one v2 bus", () => {
    t.resourceCountIs("AWS::EventsV2::EventBus", 1);
    t.hasResourceProperties("AWS::EventsV2::EventBus", { Name: "package-review", StorageConfiguration: { RetentionPeriodInDays: 1 } });
  });

  it("routes releases to the durable workflow once per CloudEvent", () => {
    t.hasResourceProperties("AWS::EventsV2::Subscriber", {
      Name: "workflow",
      Type: "FIFO",
      FilterConfiguration: { Filters: [{ Scope: "DATA", Pattern: JSON.stringify({ type: [TYPES.released] }) }] },
      InvokeConfiguration: Match.objectLike({ LambdaParameters: { InvocationType: "EVENT", DurableExecutionName: EXECUTION_NAME } }),
    });
    expect(EXECUTION_NAME).toBe("{% $events[0].Data.id %}");
  });

  it("routes review decisions to the dispatcher synchronously", () => {
    t.hasResourceProperties("AWS::EventsV2::Subscriber", {
      Name: "dispatch",
      FilterConfiguration: { Filters: [{ Scope: "DATA", Pattern: JSON.stringify({ type: [TYPES.decided] }) }] },
      InvokeConfiguration: Match.objectLike({ LambdaParameters: { InvocationType: "REQUEST_RESPONSE" } }),
    });
  });

  it("passes every event through unchanged, one per invocation", () => {
    for (const sub of Object.values(subscribers)) {
      expect(sub.Properties.Transformer).toEqual({ Type: "RAW" });
      expect(sub.Properties.BatchConfiguration).toEqual({ MaxBatchSize: 1 });
      expect(sub.Properties.FilterConfiguration.Filters[0].Scope).toBe("DATA");
    }
  });

  it("gives every subscriber a DLQ, a delivery role and a log delivery", () => {
    t.resourceCountIs("AWS::SQS::Queue", 3);
    t.resourceCountIs("AWS::Logs::Delivery", 3);
    t.hasResourceProperties("AWS::Logs::DeliverySource", { LogType: "INFO_LOGS" });
    t.hasResourceProperties("AWS::IAM::Role", {
      AssumeRolePolicyDocument: { Statement: [Match.objectLike({ Principal: { Service: "events.amazonaws.com" } })] },
    });
  });

  it("chains subscriber creation because a bus applies one at a time", () => {
    const ids = Object.keys(subscribers);
    const deps = new Map(ids.map((id) => [id, ((subscribers[id].DependsOn ?? []) as string[]).filter((d) => ids.includes(d))]));
    const roots = ids.filter((id) => deps.get(id)!.length === 0);
    const predecessors = ids.filter((id) => !roots.includes(id)).map((id) => deps.get(id)!);
    expect(ids).toHaveLength(3);
    expect(roots).toHaveLength(1);
    expect(predecessors.map((p) => p.length)).toEqual([1, 1]);
    // Two subscribers waiting on the same predecessor would still be created in parallel.
    expect(new Set(predecessors.flat()).size).toBe(predecessors.length);
  });

  it("makes the inspector durable behind an alias", () => {
    t.hasResourceProperties("AWS::Lambda::Function", { DurableConfig: Match.objectLike({ ExecutionTimeout: 172800 }) });
    t.hasResourceProperties("AWS::Lambda::Alias", { Name: "live" });
  });

  it("lets the dispatcher complete callbacks", () => {
    t.hasResourceProperties("AWS::IAM::Policy", {
      PolicyDocument: {
        Statement: Match.arrayWith([
          Match.objectLike({ Action: "lambda:SendDurableExecutionCallbackSuccess" }),
        ]),
      },
    });
  });

  it("gives every function a one-week log group that goes away with the stack", () => {
    const groups = t.findResources("AWS::Logs::LogGroup", { Properties: { RetentionInDays: 7 } });
    const functions = Object.values(t.findResources("AWS::Lambda::Function"));
    expect(functions).toHaveLength(3);
    for (const fn of functions) {
      const ref = fn.Properties.LoggingConfig?.LogGroup?.Ref;
      expect(Object.keys(groups)).toContain(ref);
      expect(groups[ref].DeletionPolicy).toBe("Delete");
    }
    t.resourceCountIs("Custom::LogRetention", 0);
  });

  it("gives only the inspector the GitHub token secret, unauthenticated until set", () => {
    const secrets = t.findResources("AWS::SecretsManager::Secret", { Properties: { SecretString: "none" } });
    const token = Object.keys(secrets).find((id) => id.startsWith("GitHubToken"))!;
    expect(token).toBeDefined();
    t.hasResourceProperties("AWS::Lambda::Function", {
      Environment: { Variables: Match.objectLike({ GITHUB_TOKEN_SECRET_ARN: { Ref: token } }) },
    });
    const readers = Object.values(t.findResources("AWS::IAM::Policy")).filter((p) =>
      JSON.stringify(p.Properties.PolicyDocument).includes(`{"Ref":"${token}"}`),
    );
    expect(readers).toHaveLength(1);
    expect(JSON.stringify(readers[0].Properties.Roles)).toMatch(/InspectorServiceRole/);
    t.hasOutput("GitHubTokenSecretArn", { Value: { Ref: token } });
  });

  it("lets the inspector publish to the bus", () => {
    t.hasResourceProperties("AWS::IAM::Policy", {
      PolicyDocument: { Statement: Match.arrayWith([Match.objectLike({ Action: "events:PutRawEvents" })]) },
    });
  });
});
