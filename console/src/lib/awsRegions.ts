// AWS commercial regions, offered by the region pickers in Settings. Static on purpose: the
// list changes a few times a year and has to work before any AWS sign-in exists. A region
// missing here is still usable through the pickers' "Other" entry, so the CP and the Agent
// must keep accepting any code — never validate against this list.
// Each code has a display name under the i18n key `ssm.region.<code>`.
export const AWS_REGIONS = [
  "us-east-1",
  "us-east-2",
  "us-west-1",
  "us-west-2",
  "af-south-1",
  "ap-east-1",
  "ap-east-2",
  "ap-south-1",
  "ap-south-2",
  "ap-northeast-1",
  "ap-northeast-2",
  "ap-northeast-3",
  "ap-southeast-1",
  "ap-southeast-2",
  "ap-southeast-3",
  "ap-southeast-4",
  "ap-southeast-5",
  "ap-southeast-6",
  "ap-southeast-7",
  "ca-central-1",
  "ca-west-1",
  "eu-central-1",
  "eu-central-2",
  "eu-west-1",
  "eu-west-2",
  "eu-west-3",
  "eu-north-1",
  "eu-south-1",
  "eu-south-2",
  "il-central-1",
  "me-south-1",
  "me-central-1",
  "mx-central-1",
  "sa-east-1",
] as const;

export type AwsRegion = (typeof AWS_REGIONS)[number];

export function isListedAwsRegion(code: string): code is AwsRegion {
  return (AWS_REGIONS as readonly string[]).includes(code);
}
