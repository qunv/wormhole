import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Boxes, ChevronLeft, Code2, FolderGit2, KeyRound, LayoutDashboard, Moon, Settings2, Sun } from "lucide-react";
import { api } from "./api";
import { Configuration } from "./Configuration";
import { Overview } from "./Overview";
import { Secrets } from "./Secrets";
import { Workspaces } from "./Workspaces";

type Page = "overview" | "configuration" | "workspaces" | "secrets";
const nav: { id: Page; label: string; icon: React.ReactNode; description: string }[] = [
  { id: "overview", label: "Overview", icon: <LayoutDashboard size={19} />, description: "Health and posture" },
  { id: "configuration", label: "Configuration", icon: <Settings2 size={19} />, description: "Global runtime settings" },
  { id: "workspaces", label: "Workspaces", icon: <FolderGit2 size={19} />, description: "Named overrides" },
  { id: "secrets", label: "Secrets", icon: <KeyRound size={19} />, description: "Write-only values" },
];

export default function App() {
  const bootstrap = useQuery({ queryKey: ["bootstrap"], queryFn: api.bootstrap });
  const [page, setPage] = useState<Page>(() => (window.location.hash.slice(1) as Page) || "overview");
  const [collapsed, setCollapsed] = useState(false);
  const [dark, setDark] = useState(() => localStorage.getItem("codebridge-theme") !== "light");

  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
    localStorage.setItem("codebridge-theme", dark ? "dark" : "light");
  }, [dark]);
  useEffect(() => { window.location.hash = page; }, [page]);

  return <div className={`app-shell ${collapsed ? "sidebar-collapsed" : ""}`}>
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark"><Code2 size={22} /></span><span className="brand-copy"><strong>Codebridge</strong><small>Admin Console</small></span></div>
      <nav className="main-nav">{nav.map((item) => <button key={item.id} className={page === item.id ? "active" : ""} onClick={() => setPage(item.id)} title={collapsed ? item.label : undefined}>{item.icon}<span><strong>{item.label}</strong><small>{item.description}</small></span></button>)}</nav>
      <div className="sidebar-footer">
        <div className="daemon-chip"><span className="status-dot" /><Boxes size={16} /><span><strong>Local daemon</strong><small>{bootstrap.data ? `v${bootstrap.data.version}` : "Connecting…"}</small></span></div>
        <button className="collapse-button" onClick={() => setCollapsed(!collapsed)} aria-label="Toggle sidebar"><ChevronLeft size={17} /></button>
      </div>
    </aside>
    <main className="main-area">
      <div className="topbar"><div className="crumb"><span>Codebridge</span><i>/</i><strong>{nav.find((item) => item.id === page)?.label}</strong></div><button className="theme-button" onClick={() => setDark(!dark)} aria-label="Toggle color theme">{dark ? <Sun size={17} /> : <Moon size={17} />}</button></div>
      <div className="page-container">{page === "overview" && <Overview />}{page === "configuration" && <Configuration />}{page === "workspaces" && <Workspaces />}{page === "secrets" && <Secrets />}</div>
    </main>
  </div>;
}
