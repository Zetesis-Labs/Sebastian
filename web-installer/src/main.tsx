import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "esp-web-tools/dist/web/install-button.js";
import "./index.css";
import App from "./App";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
