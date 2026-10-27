import { useQuery } from "@tanstack/react-query";
import { getAuthProfile } from "./apiAuth";

/** useAuthProfile resolves the caller's identity (principal + tenant).
 *
 *  Shares the ["auth-profile", token] query key with App's own check on purpose:
 *  one cache entry means the workspace gate and the pages that read roles agree on
 *  who the caller is, instead of issuing two lookups that could briefly disagree.
 *
 *  Enabled whenever a credential exists — NOT only for sessions. A page that needs
 *  the principal's platform role must work for the shared web token too (that
 *  holder IS the platform admin), and gating on "session" would leave it blank. */
export function useAuthProfile(token: string | null) {
  return useQuery({
    queryKey: ["auth-profile", token],
    enabled: (token ?? "").trim().length > 0,
    queryFn: () => getAuthProfile(token),
    retry: false,
  });
}
