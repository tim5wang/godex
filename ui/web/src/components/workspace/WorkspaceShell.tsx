import type { ReactNode } from "react";
import { Layout } from "antd";

export type WorkspaceShellProps = {
  shellClassName?: string;
  appNav?: ReactNode;
  header: ReactNode;
  content: ReactNode;
  drawer?: ReactNode;
};

export function buildWorkspaceShellClassName(routeShellClassName?: string): string {
  return ["godex-shell", routeShellClassName].filter(Boolean).join(" ");
}

export function WorkspaceShell(props: WorkspaceShellProps) {
  return (
    <Layout className={props.shellClassName || buildWorkspaceShellClassName()}>
      {props.appNav ?? null}
      <Layout>
        {props.header}
        {props.content}
      </Layout>
      {props.drawer ?? null}
    </Layout>
  );
}
