import { ArnFormat, Duration, RemovalPolicy, Stack } from "aws-cdk-lib";
import * as eventsv2 from "aws-cdk-lib/aws-eventsv2";
import * as iam from "aws-cdk-lib/aws-iam";
import * as lambda from "aws-cdk-lib/aws-lambda";
import * as logs from "aws-cdk-lib/aws-logs";
import * as sqs from "aws-cdk-lib/aws-sqs";
import { Construct } from "constructs";
import { EventBusV2 } from "./eventbus-v2";

export interface LambdaParameters {
  readonly InvocationType?: "EVENT" | "REQUEST_RESPONSE";
  readonly DurableExecutionName?: string;
}

export interface SubscriberV2Props {
  readonly bus: EventBusV2;
  readonly subscriberName: string;
  /** A function, alias or version; an alias ARN makes a separate Qualifier unnecessary. */
  readonly target: lambda.IFunction;
  readonly ordering?: "FIFO" | "UNORDERED";
  /** Event pattern matched against the CloudEvent body. */
  readonly dataPattern: Record<string, unknown>;
  readonly lambdaParameters?: LambdaParameters;
}

export class SubscriberV2 extends Construct {
  readonly resource: eventsv2.CfnSubscriber;
  readonly subscriberArn: string;
  readonly deadLetterQueue: sqs.Queue;
  readonly logGroup: logs.LogGroup;

  constructor(scope: Construct, id: string, props: SubscriberV2Props) {
    super(scope, id);
    this.deadLetterQueue = new sqs.Queue(this, "Dlq", { retentionPeriod: Duration.days(14) });
    const role = new iam.Role(this, "DeliveryRole", { assumedBy: new iam.ServicePrincipal("events.amazonaws.com") });
    props.target.grantInvoke(role);
    this.deadLetterQueue.grantSendMessages(role);

    this.resource = new eventsv2.CfnSubscriber(this, "Resource", {
      name: props.subscriberName,
      eventBusArn: props.bus.busArn,
      type: props.ordering ?? "UNORDERED",
      filterConfiguration: { filters: [{ scope: "DATA", pattern: JSON.stringify(props.dataPattern) }] },
      transformer: { type: "RAW" },
      batchConfiguration: { maxBatchSize: 1 },
      retryPolicy: { maxRetryAttempts: 5, maxEventAgeInSeconds: 3600 },
      onFailureConfiguration: { arn: this.deadLetterQueue.queueArn },
      logConfiguration: { level: "INFO", includePayload: "FULL" },
      state: "RUNNING",
    });
    // CDK 2.272.0's CfnSubscriber has no InvokeConfiguration property, so the target goes in raw.
    this.resource.addPropertyOverride("InvokeConfiguration", {
      TargetArn: props.target.functionArn,
      RoleArn: role.roleArn,
      LambdaParameters: props.lambdaParameters,
    });
    // The delivery role's policy must exist before the subscriber can deliver.
    this.resource.node.addDependency(role);
    this.subscriberArn = this.resource.attrSubscriberArn;

    this.logGroup = new logs.LogGroup(this, "Logs", {
      logGroupName: `/aws/vendedlogs/eventsv2/${props.bus.busName}/${props.subscriberName}`,
      retention: logs.RetentionDays.ONE_WEEK,
      removalPolicy: RemovalPolicy.DESTROY,
    });
    const name = `${props.bus.busName}-${props.subscriberName}`;
    const source = new logs.CfnDeliverySource(this, "LogSource", { name: `${name}-src`, resourceArn: this.subscriberArn, logType: "INFO_LOGS" });
    const destination = new logs.CfnDeliveryDestination(this, "LogDestination", {
      name: `${name}-dst`,
      destinationResourceArn: Stack.of(this).formatArn({
        service: "logs",
        resource: "log-group",
        resourceName: this.logGroup.logGroupName,
        arnFormat: ArnFormat.COLON_RESOURCE_NAME,
      }),
    });
    const delivery = new logs.CfnDelivery(this, "LogDelivery", { deliverySourceName: source.name, deliveryDestinationArn: destination.attrArn });
    delivery.addResourceDependency(source);
  }
}
