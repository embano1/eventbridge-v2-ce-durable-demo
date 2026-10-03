import * as eventsv2 from "aws-cdk-lib/aws-eventsv2";
import * as iam from "aws-cdk-lib/aws-iam";
import { Construct } from "constructs";

export interface EventBusV2Props {
  readonly busName: string;
  readonly description?: string;
  readonly retentionDays?: number;
}

export class EventBusV2 extends Construct {
  readonly resource: eventsv2.CfnEventBus;
  readonly busArn: string;
  readonly busName: string;

  constructor(scope: Construct, id: string, props: EventBusV2Props) {
    super(scope, id);
    this.busName = props.busName;
    this.resource = new eventsv2.CfnEventBus(this, "Resource", {
      name: props.busName,
      description: props.description,
      storageConfiguration: { retentionPeriodInDays: props.retentionDays ?? 1 },
    });
    this.busArn = this.resource.attrEventBusArn;
  }

  grantPublish(grantee: iam.IGrantable): iam.Grant {
    return iam.Grant.addToPrincipal({ grantee, actions: ["events:PutRawEvents"], resourceArns: [this.busArn] });
  }
}
