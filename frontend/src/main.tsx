import { createRoot } from "react-dom/client";
import "@fontsource-variable/geist";
import "@fontsource-variable/geist-mono";
import "@xterm/xterm/css/xterm.css";
import "./styles.css";
import { App } from "./components/App";

createRoot(document.getElementById("root")!).render(<App />);
