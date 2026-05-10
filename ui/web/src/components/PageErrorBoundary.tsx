import { Component, type ErrorInfo, type ReactNode } from "react";
import { Alert, Button, Space, Typography } from "antd";

interface PageErrorBoundaryProps {
  appName: string;
  children: ReactNode;
}

interface PageErrorBoundaryState {
  error: Error | null;
}

export class PageErrorBoundary extends Component<PageErrorBoundaryProps, PageErrorBoundaryState> {
  state: PageErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): PageErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(`GoDex app "${this.props.appName}" crashed`, error, info);
  }

  componentDidUpdate(prevProps: PageErrorBoundaryProps) {
    if (prevProps.appName !== this.props.appName && this.state.error) {
      this.setState({ error: null });
    }
  }

  render() {
    if (!this.state.error) {
      return this.props.children;
    }
    return (
      <main className="page-shell">
        <Alert
          type="error"
          showIcon
          message={`The ${this.props.appName} page crashed.`}
          description={
            <Space direction="vertical" size={12}>
              <Typography.Text>{this.state.error.message || "Unexpected UI error."}</Typography.Text>
              <Button onClick={() => this.setState({ error: null })}>Try again</Button>
            </Space>
          }
        />
      </main>
    );
  }
}
