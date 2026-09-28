import { Component, type ReactNode } from "react";

interface State { err: Error | null }

/** ErrorBoundary keeps one broken panel from blanking the whole window. */
export class ErrorBoundary extends Component<{ children: ReactNode; label: string }, State> {
  state: State = { err: null };

  static getDerivedStateFromError(err: Error): State {
    return { err };
  }

  componentDidCatch(err: Error) {
    console.error(`[st8ks] ${this.props.label} failed:`, err);
  }

  render() {
    if (!this.state.err) return this.props.children;
    return (
      <div className="center">
        <div className="col" style={{ gap: 10, maxWidth: 520, alignItems: "flex-start" }}>
          <span style={{ fontWeight: 600 }}>The {this.props.label} stopped because of an error.</span>
          <div className="errbox" style={{ width: "100%" }}>{this.state.err.message}</div>
          <button className="btn" onClick={() => this.setState({ err: null })}>Try again</button>
        </div>
      </div>
    );
  }
}
