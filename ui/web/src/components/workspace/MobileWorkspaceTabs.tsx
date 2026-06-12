import { useMemo } from "react";
import { Grid, Segmented } from "antd";
import {
  selectMobileWorkspaceTabs,
  useLayoutStore,
  type MobileWorkspaceTabsSnapshot,
} from "../../store/layout";
import { useI18n } from "../../i18n";

// P0-D / T9 (SPEC §3.3): Secondary workspace tab bar for screens < 1024px.
// Shows 5 tabs: chat | terminal | files | drawer | tasks. Active tab is
// driven by useLayoutStore.mobileActiveTab. On >= 1024px the component
// returns null — primary navigation there is the 2x2 grid preset
// (CenterGrid) and AppNav is unchanged. This component is intentionally
// thin: all state is in the store, the selector is the contract.
//
// Visibility is decided by the renderer via Grid.useBreakpoint; that is
// UI concern, not store concern (so we don't push viewport state into
// the layout store).
export function MobileWorkspaceTabs() {
  const screens = Grid.useBreakpoint();
  // Calling these as separate selectors keeps the store granular so
  // unrelated panel changes don't re-render this component.
  const snap = useLayoutStore(selectMobileWorkspaceTabs);
  const setMobileActiveTab = useLayoutStore((state) => state.setMobileActiveTab);
  const { t } = useI18n();
  const options = useMemo(
    () =>
      snap.tabs.map((tab) => ({
        label: t(tab.i18nKey),
        value: tab.key,
      })),
    [snap.tabs, t],
  );

  if (screens.lg) {
    return null;
  }

  return (
    <div
      data-testid="mobile-workspace-tabs"
      data-active={snap.active}
      className="border-b border-[color:var(--border)] bg-[color:var(--panel)] px-3 py-2"
    >
      <Segmented<MobileWorkspaceTabsSnapshot["active"]>
        block
        size="middle"
        value={snap.active}
        onChange={(value) => setMobileActiveTab(value)}
        options={options}
      />
    </div>
  );
}
