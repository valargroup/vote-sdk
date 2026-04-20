import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import App from "./App";
import { UIConfigProvider } from "./store/uiConfig";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <UIConfigProvider>
      <App />
    </UIConfigProvider>
  </StrictMode>
);
