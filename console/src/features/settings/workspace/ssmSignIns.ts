// Shared IAM Identity Center sign-ins (ADR 0125). The Settings UI that lists and edits them
// comes with PR 5 of #3014; the type is declared here first so the contract test binds it to the
// Control Plane's ssmSignInDTO from the PR that adds the route.

/** One shared sign-in, as GET /api/ssm/sign-ins lists it. */
export interface SsmSignIn {
  id: string;
  name: string;
  startUrl: string;
  ssoRegion: string;
  createdAt: string;
  /** The ids of the profiles in it. */
  profiles: string[];
}
