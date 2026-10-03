import * as path from "node:path";
import { CfnOutput, Duration, RemovalPolicy, SecretValue, Stack, StackProps } from "aws-cdk-lib";
import * as iam from "aws-cdk-lib/aws-iam";
import * as lambda from "aws-cdk-lib/aws-lambda";
import * as nodejs from "aws-cdk-lib/aws-lambda-nodejs";
import * as logs from "aws-cdk-lib/aws-logs";
import * as secretsmanager from "aws-cdk-lib/aws-secretsmanager";
import { Construct } from "constructs";
import { EventBusV2 } from "./eventbus-v2";
import { SubscriberV2 } from "./subscriber-v2";

const repoRoot = path.resolve(__dirname, "..", "..");

export const BUS_NAME = "package-review";

export const TYPES = {
  released: "dev.example.package.released",
  requested: "dev.example.review.requested",
  decided: "dev.example.review.decided",
  verdict: "dev.example.package.verdict",
};

// The CloudEvent id names the durable execution, so a repeated delivery joins the existing one.
export const EXECUTION_NAME = "{% $events[0].Data.id %}";

export class DemoStack extends Stack {
  constructor(scope: Construct, id: string, props: StackProps = {}) {
    super(scope, id, props);

    const bus = new EventBusV2(this, "Bus", { busName: BUS_NAME, description: "EventBridge v2 + durable functions demo", retentionDays: 1 });
    const jevKey = new secretsmanager.Secret(this, "JevApiKey", {
      description: "Jev API key; the value 'none' selects the offline heuristic classifier",
      secretStringValue: SecretValue.unsafePlainText("none"),
    });

    const githubToken = new secretsmanager.Secret(this, "GitHubToken", {
      description: "GitHub token for the compare API; the value 'none' calls GitHub unauthenticated",
      secretStringValue: SecretValue.unsafePlainText("none"),
    });

    // Explicit log groups expire and go away with the stack; logRetention would add a custom resource.
    const logGroup = (id: string) => new logs.LogGroup(this, id, { retention: logs.RetentionDays.ONE_WEEK, removalPolicy: RemovalPolicy.DESTROY });
    const inspectorLogs = logGroup("InspectorLogs");
    const trailLogs = logGroup("TrailLogs");
    const dispatcherLogs = logGroup("DispatcherLogs");

    const inspector = goFunction(this, "Inspector", "inspector", {
      timeout: Duration.minutes(1),
      durableConfig: { executionTimeout: Duration.days(2), retentionPeriod: Duration.days(7) },
      environment: { BUS_ARN: bus.busArn, JEV_SECRET_ARN: jevKey.secretArn, GITHUB_TOKEN_SECRET_ARN: githubToken.secretArn },
      logGroup: inspectorLogs,
    });
    bus.grantPublish(inspector);
    jevKey.grantRead(inspector);
    githubToken.grantRead(inspector);
    const live = new lambda.Alias(this, "InspectorLive", { aliasName: "live", version: inspector.currentVersion });

    const trail = goFunction(this, "Trail", "trail", { timeout: Duration.seconds(10), logGroup: trailLogs });

    const dispatcherDir = path.join(repoRoot, "functions", "dispatcher");
    const dispatcher = new nodejs.NodejsFunction(this, "Dispatcher", {
      entry: path.join(dispatcherDir, "src", "handler.ts"),
      projectRoot: dispatcherDir,
      depsLockFilePath: path.join(dispatcherDir, "package-lock.json"),
      runtime: lambda.Runtime.NODEJS_24_X,
      architecture: lambda.Architecture.ARM_64,
      loggingFormat: lambda.LoggingFormat.JSON,
      timeout: Duration.seconds(10),
      logGroup: dispatcherLogs,
      // The runtime's bundled SDK can predate the durable execution callback commands.
      bundling: { bundleAwsSDK: true },
    });
    dispatcher.addToRolePolicy(
      new iam.PolicyStatement({
        actions: ["lambda:SendDurableExecutionCallbackSuccess"],
        resources: [inspector.functionArn, `${inspector.functionArn}:*`],
      }),
    );

    const subscribers = [
      new SubscriberV2(this, "WorkflowSubscriber", {
        bus,
        subscriberName: "workflow",
        target: live,
        ordering: "FIFO",
        dataPattern: { type: [TYPES.released] },
        lambdaParameters: { InvocationType: "EVENT", DurableExecutionName: EXECUTION_NAME },
      }),
      new SubscriberV2(this, "DispatchSubscriber", {
        bus,
        subscriberName: "dispatch",
        target: dispatcher,
        dataPattern: { type: [TYPES.decided] },
        lambdaParameters: { InvocationType: "REQUEST_RESPONSE" },
      }),
      new SubscriberV2(this, "TrailSubscriber", {
        bus,
        subscriberName: "trail",
        target: trail,
        dataPattern: { type: Object.values(TYPES) },
        lambdaParameters: { InvocationType: "EVENT" },
      }),
    ];
    // A bus applies one subscriber create or delete at a time (ConcurrentModificationException otherwise).
    subscribers.reduce((prev, cur) => {
      cur.node.addDependency(prev);
      return cur;
    });

    new CfnOutput(this, "BusArn", { value: bus.busArn });
    new CfnOutput(this, "InspectorFunctionName", { value: inspector.functionName });
    new CfnOutput(this, "JevSecretArn", { value: jevKey.secretArn });
    new CfnOutput(this, "GitHubTokenSecretArn", { value: githubToken.secretArn });
    new CfnOutput(this, "InspectorLogGroup", { value: inspectorLogs.logGroupName });
    new CfnOutput(this, "DispatcherLogGroup", { value: dispatcherLogs.logGroupName });
    new CfnOutput(this, "TrailLogGroup", { value: trailLogs.logGroupName });
  }
}

function goFunction(scope: Construct, id: string, name: string, props: Partial<lambda.FunctionProps>): lambda.Function {
  return new lambda.Function(scope, id, {
    runtime: lambda.Runtime.PROVIDED_AL2023,
    architecture: lambda.Architecture.ARM_64,
    handler: "bootstrap",
    code: lambda.Code.fromAsset(path.join(repoRoot, "build", name)),
    memorySize: 256,
    loggingFormat: lambda.LoggingFormat.JSON,
    ...props,
  });
}
