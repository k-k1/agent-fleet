// Validation of an assume-role Settings profile (issue #1109): the same shapes as the CP's
// validateProfile (control-plane/ssm.go) and the Agent's ValidateAssumeRole, so Save and the
// settings import accept exactly what the backend does. Duration is 900..3600 because a role
// assumed from an Identity Center sign-in is role chaining, which AWS limits to one hour.
export function validRoleArn(arn: string): boolean {
  if (!/^arn:aws[a-z-]*:iam::\d{12}:role\/[A-Za-z0-9+=,.@_/-]{1,512}$/.test(arn) || arn.endsWith("/") || arn.includes("//")) return false;
  return arn.length - arn.lastIndexOf("/") - 1 <= 64;
}
export const validExternalId = (v: string): boolean => v === "" || (v.length <= 1224 && /^[A-Za-z0-9+=,.@:/_-]{2,}$/.test(v));
export const validSessionName = (v: string): boolean => v === "" || /^[A-Za-z0-9+=,.@_-]{2,64}$/.test(v);
export const validDuration = (n: number): boolean => Number.isInteger(n) && n >= 900 && n <= 3600;
