import { App } from "aws-cdk-lib";
import { DemoStack } from "../lib/demo-stack";

const app = new App();
new DemoStack(app, "EbV2DemoStack", { env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: process.env.CDK_DEFAULT_REGION ?? "eu-central-1" } });
