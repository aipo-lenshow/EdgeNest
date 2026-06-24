import React from "react";
import ReactDOM from "react-dom/client";
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from "@tanstack/react-query";
import {
  BrowserRouter,
  Routes,
  Route,
  Navigate,
  useLocation,
} from "react-router-dom";
import "./index.css";
import "./i18n/i18n";
import { initTheme } from "./lib/theme";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Connections from "./pages/Connections";
import Outbound from "./pages/Outbound";
import Inbound from "./pages/Inbound";
import Firewall from "./pages/Firewall";
import Certs from "./pages/Certs";
import Monitor from "./pages/Monitor";
import Password from "./pages/Password";
import ProtocolGuide from "./pages/ProtocolGuide";
import CreateInbound from "./pages/CreateInbound";
import About from "./pages/About";
import Settings from "./pages/Settings";
import { api, call, getToken } from "./api/client";

const qc = new QueryClient();

initTheme();

interface Me {
  username: string;
  must_change_password: boolean;
  wizard_done: boolean;
  run_mode: string;
}

// Guarded route wrapper. Forces /password while the bootstrap flag is set,
// /login when there's no token — anything else passes through.
function Protected({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  if (!getToken()) return <Navigate to="/login" replace />;
  const { data, isLoading } = useQuery({
    queryKey: ["me"],
    queryFn: () => call<Me>(api.get("/me")),
    retry: false,
  });
  if (isLoading) return null;
  if (data?.must_change_password && location.pathname !== "/password") {
    return <Navigate to="/password" replace />;
  }
  return <>{children}</>;
}

// FixedRedirect sends a retired route to a fixed new path, carrying over any
// query string except `tab` (the target already encodes its tab). Keeps deep
// links like /warp?preset=ai working → /outbound?tab=warp&preset=ai.
function FixedRedirect({ to }: { to: string }) {
  const location = useLocation();
  const incoming = new URLSearchParams(location.search);
  const [path, q] = to.split("?");
  const merged = new URLSearchParams(q);
  incoming.forEach((v, k) => {
    if (k !== "tab") merged.set(k, v);
  });
  const qs = merged.toString();
  return <Navigate to={qs ? `${path}?${qs}` : path} replace />;
}

// RelayLegacyRedirect maps the retired unified /relay?tab=… hub onto the new
// direction-split pages: tab=advanced → /inbound, tab=detect|warp → /outbound
// (preserving the tab + any ?preset=).
function RelayLegacyRedirect() {
  const location = useLocation();
  const params = new URLSearchParams(location.search);
  const tab = params.get("tab");
  params.delete("tab");
  if (tab === "advanced") {
    const rest = params.toString();
    return <Navigate to={rest ? `/inbound?${rest}` : "/inbound"} replace />;
  }
  const target = tab === "warp" ? "warp" : tab === "detect" ? "detect" : "routes";
  params.set("tab", target);
  return <Navigate to={`/outbound?${params.toString()}`} replace />;
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={qc}>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/password" element={<Protected><Password /></Protected>} />
          <Route path="/" element={<Protected><Dashboard /></Protected>} />
          <Route path="/guide" element={<Protected><ProtocolGuide /></Protected>} />
          <Route path="/create-inbound" element={<Protected><CreateInbound /></Protected>} />
          <Route path="/connections" element={<Protected><Connections /></Protected>} />
          <Route path="/inbounds" element={<Protected><FixedRedirect to="/connections?tab=inbounds" /></Protected>} />
          <Route path="/outbound" element={<Protected><Outbound /></Protected>} />
          <Route path="/inbound" element={<Protected><Inbound /></Protected>} />
          <Route path="/routes" element={<Protected><FixedRedirect to="/outbound?tab=routes" /></Protected>} />
          <Route path="/relay" element={<Protected><RelayLegacyRedirect /></Protected>} />
          <Route path="/warp" element={<Protected><FixedRedirect to="/outbound?tab=warp" /></Protected>} />
          <Route path="/advanced" element={<Protected><FixedRedirect to="/inbound" /></Protected>} />
          <Route path="/firewall" element={<Protected><Firewall /></Protected>} />
          <Route path="/cloud-firewall" element={<Protected><FixedRedirect to="/firewall" /></Protected>} />
          <Route path="/unlock" element={<Protected><FixedRedirect to="/outbound?tab=detect" /></Protected>} />
          <Route path="/certs" element={<Protected><Certs /></Protected>} />
          <Route path="/subscriptions" element={<Protected><FixedRedirect to="/connections?tab=subscriptions" /></Protected>} />
          <Route path="/stats" element={<Protected><Monitor /></Protected>} />
          <Route path="/audit" element={<Protected><FixedRedirect to="/stats?tab=audit" /></Protected>} />
          <Route path="/protocols" element={<Protected><FixedRedirect to="/guide" /></Protected>} />
          <Route path="/system" element={<Protected><FixedRedirect to="/" /></Protected>} />
          <Route path="/about" element={<Protected><About /></Protected>} />
          <Route path="/settings" element={<Protected><Settings /></Protected>} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>
);
