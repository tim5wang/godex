/** Labels shared by the user centre and member management.
 *
 *  Kept in its own module rather than imported page-to-page: when MembersPage
 *  imported these from AccountPage, Rollup merged the two into a single shared
 *  chunk — so opening /members also shipped the user-centre code, and AccountPage
 *  stopped being its own route entry (which also dropped it out of the cold-load
 *  bundle budget check). A leaf module keeps each page's chunk independent. */

/** roleLabel renders a tenant role. An unknown role shows as-is rather than being
 *  hidden: a role the UI does not know about is a fact the user should see. */
export function roleLabel(t: (key: string) => string, role?: string): string {
  switch (role) {
    case "owner":
      return t("members.roleOwner");
    case "admin":
      return t("members.roleAdmin");
    case "member":
      return t("members.roleMember");
    default:
      return role || "-";
  }
}

/** eventLabel renders one sign-in-history event kind. */
export function eventLabel(t: (key: string) => string, event: string): string {
  switch (event) {
    case "register":
      return t("account.eventRegister");
    case "login":
      return t("account.eventLogin");
    case "logout":
      return t("account.eventLogout");
    case "password_changed":
      return t("account.eventPasswordChanged");
    case "sessions_revoked":
      return t("account.eventSessionsRevoked");
    case "session_revoked":
      return t("account.eventSessionRevoked");
    case "joined_tenant":
      return t("account.eventJoinedTenant");
    default:
      return event;
  }
}
