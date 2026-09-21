import { useEffect, useState } from "react";
import { Tabs } from "antd";
import { useLocation } from "react-router-dom";
import { useI18n } from "../../i18n";
import { BusinessAgentsPage } from "../business-agents/BusinessAgentsPage";
import { FlowsPage } from "../flows/FlowsPage";

const AGENT_TAB = "agents";
const FLOW_TAB = "flows";

function initialTabForPath(pathname: string): string {
  // Legacy routes (/flows, /business-agents) land on the corresponding tab
  // so old bookmarks keep working after the navigation merge.
  if (pathname.startsWith("/flows")) {
    return FLOW_TAB;
  }
  if (pathname.startsWith("/business-agents")) {
    return AGENT_TAB;
  }
  return AGENT_TAB;
}

/**
 * OrchestrationPage is the single business-orchestration entry (design doc
 * §16 decision 6): the agent library and the flow library sit side by side
 * as grouped tabs under one navigation item — not two separate apps, and
 * flows are not nested inside a single agent's detail page (a flow spans
 * multiple agents).
 */
export function OrchestrationPage() {
  const { t } = useI18n();
  const location = useLocation();
  const [active, setActive] = useState<string>(() => initialTabForPath(location.pathname));

  // Keep the tab in sync when navigating between legacy routes.
  useEffect(() => {
    setActive(initialTabForPath(location.pathname));
  }, [location.pathname]);

  return (
    <div className="orchestration-page">
      <Tabs
        activeKey={active}
        onChange={setActive}
        items={[
          {
            key: AGENT_TAB,
            label: t("orchestration.tabAgents"),
            children: <BusinessAgentsPage />,
          },
          {
            key: FLOW_TAB,
            label: t("orchestration.tabFlows"),
            children: <FlowsPage />,
          },
        ]}
      />
    </div>
  );
}
