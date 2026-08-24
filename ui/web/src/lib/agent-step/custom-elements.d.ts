/**
 * JSX typing for the <godex-step> custom element (Agent Step Platform Phase C).
 *
 * React 19's JSX namespace resolves through the `react` module, so the custom
 * element is registered by augmenting `JSX.IntrinsicElements` there. Attributes
 * map to the GodexStepElement observed attributes (kebab-case in markup, which
 * React accepts as-is on custom elements).
 */
import type * as React from "react";

declare module "react" {
  namespace JSX {
    interface IntrinsicElements {
      "godex-step": React.DetailedHTMLProps<React.HTMLAttributes<HTMLElement>, HTMLElement> & {
        "base-url"?: string;
        "api-key"?: string;
        prompt?: string;
        placeholder?: string;
        inputs?: string;
        context?: string;
        tools?: string;
      };
    }
  }
}
