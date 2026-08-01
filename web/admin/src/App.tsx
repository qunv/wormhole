import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Boxes,
  ChevronLeft,
  Code2,
  FolderGit2,
  KeyRound,
  LayoutDashboard,
  LoaderCircle,
  LockKeyhole,
  LogIn,
  LogOut,
  Moon,
  Rocket,
  Settings2,
  Wrench,
  ShieldCheck,
  Sun,
  Terminal,
  UserRound,
} from "lucide-react";
import { api, APIError, AUTH_REQUIRED_EVENT } from "./api";
import { Button, Notice, TextInput } from "./components";
import { Configuration } from "./Configuration";
import { Overview } from "./Overview";
import { Profiles } from "./Profiles";
import { Secrets } from "./Secrets";
import { Setup } from "./Setup";
import { Workspaces } from "./Workspaces";
import type { AdminAuthStatus } from "./types";

type Page = "setup" | "overview" | "profiles" | "configuration" | "workspaces" | "secrets";
const nav: { id: Page; label: string; icon: React.ReactNode; description: string }[] = [
  { id: "setup", label: "Setup", icon: <Rocket size={19} />, description: "Guided first run" },
  { id: "overview", label: "Overview", icon: <LayoutDashboard size={19} />, description: "Health and posture" },
  { id: "profiles", label: "Profiles", icon: <Wrench size={19} />, description: "Exposed tool contracts" },
  { id: "configuration", label: "Configuration", icon: <Settings2 size={19} />, description: "Global runtime settings" },
  { id: "workspaces", label: "Workspaces", icon: <FolderGit2 size={19} />, description: "Named overrides" },
  { id: "secrets", label: "Secrets", icon: <KeyRound size={19} />, description: "Write-only values" },
];

function pageFromHash(): Page {
  const candidate = window.location.hash.slice(1).split("/", 1)[0];
  return nav.some((item) => item.id === candidate) ? candidate as Page : "overview";
}

export default function App() {
  const queryClient = useQueryClient();
  const auth = useQuery({ queryKey: ["admin-auth"], queryFn: api.authStatus, retry: false, staleTime: 0 });
  const bootstrap = useQuery({
    queryKey: ["bootstrap"],
    queryFn: api.bootstrap,
    enabled: auth.data?.authenticated === true,
  });
  const [page, setPage] = useState<Page>(pageFromHash);
  const [collapsed, setCollapsed] = useState(false);
  const [dark, setDark] = useState(() => localStorage.getItem("codebridge-theme") !== "light");

  const logout = useMutation({
    mutationFn: api.logout,
    onSuccess: () => {
      queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== "admin-auth" });
      queryClient.setQueryData<AdminAuthStatus>(["admin-auth"], (current) => current ? { ...current, authenticated: false } : current);
    },
  });

  useEffect(() => {
    document.documentElement.dataset.theme = dark ? "dark" : "light";
    localStorage.setItem("codebridge-theme", dark ? "dark" : "light");
  }, [dark]);
  useEffect(() => {
    const syncPage = () => setPage(pageFromHash());
    window.addEventListener("hashchange", syncPage);
    if (!window.location.hash) window.history.replaceState(null, "", "#overview");
    return () => window.removeEventListener("hashchange", syncPage);
  }, []);
  useEffect(() => {
    const requireLogin = () => {
      queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== "admin-auth" });
      void queryClient.invalidateQueries({ queryKey: ["admin-auth"] });
    };
    window.addEventListener(AUTH_REQUIRED_EVENT, requireLogin);
    return () => window.removeEventListener(AUTH_REQUIRED_EVENT, requireLogin);
  }, [queryClient]);

  const navigatePage = (nextPage: Page) => {
    if (window.location.hash !== `#${nextPage}`) window.location.hash = nextPage;
  };

  if (auth.isLoading) return <AuthLoading />;
  if (auth.error || !auth.data) {
    return <AuthFrame dark={dark} onToggleTheme={() => setDark(!dark)}>
      <div className="auth-state-icon danger"><LockKeyhole size={25} /></div>
      <span className="auth-eyebrow">Admin authentication</span>
      <h1>Unable to check login status</h1>
      <p>{errorMessage(auth.error) || "The local Codebridge daemon did not return an authentication status."}</p>
      <Button onClick={() => void auth.refetch()}>Retry</Button>
    </AuthFrame>;
  }
  if (!auth.data.configured) {
    return <SetupRequired
      status={auth.data}
      dark={dark}
      onToggleTheme={() => setDark(!dark)}
      onConfigured={() => {
        window.location.hash = "setup";
        void auth.refetch();
      }}
    />;
  }
  if (!auth.data.authenticated) {
    return <LoginScreen
      status={auth.data}
      dark={dark}
      onToggleTheme={() => setDark(!dark)}
      onLoggedIn={() => void auth.refetch()}
    />;
  }

  return <div className={`app-shell ${collapsed ? "sidebar-collapsed" : ""}`}>
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark"><Code2 size={22} /></span><span className="brand-copy"><strong>Codebridge</strong><small>Admin Console</small></span></div>
      <nav className="main-nav">{nav.map((item) => <button key={item.id} className={page === item.id ? "active" : ""} onClick={() => navigatePage(item.id)} title={collapsed ? item.label : undefined}>{item.icon}<span><strong>{item.label}</strong><small>{item.description}</small></span></button>)}</nav>
      <div className="sidebar-footer">
        <div className="daemon-chip"><span className="status-dot" /><Boxes size={16} /><span><strong>Local daemon</strong><small>{bootstrap.data ? `v${bootstrap.data.version}` : "Connecting…"}</small></span></div>
        <button className="collapse-button" onClick={() => setCollapsed(!collapsed)} aria-label="Toggle sidebar"><ChevronLeft size={17} /></button>
      </div>
    </aside>
    <main className="main-area">
      <div className="topbar">
        <div className="crumb"><span>Codebridge</span><i>/</i><strong>{nav.find((item) => item.id === page)?.label}</strong></div>
        <div className="topbar-actions">
          <span className="admin-session"><UserRound size={15} /><span>{auth.data.username}</span></span>
          <button className="theme-button" onClick={() => setDark(!dark)} aria-label="Toggle color theme">{dark ? <Sun size={17} /> : <Moon size={17} />}</button>
          <button className="theme-button" onClick={() => logout.mutate()} disabled={logout.isPending} aria-label="Sign out">{logout.isPending ? <LoaderCircle size={17} className="spin" /> : <LogOut size={17} />}</button>
        </div>
      </div>
      <div className="page-container">{page === "setup" && <Setup />}{page === "overview" && <Overview />}{page === "profiles" && <Profiles />}{page === "configuration" && <Configuration />}{page === "workspaces" && <Workspaces />}{page === "secrets" && <Secrets />}</div>
    </main>
  </div>;
}

function LoginScreen({ status, dark, onToggleTheme, onLoggedIn }: {
  status: AdminAuthStatus;
  dark: boolean;
  onToggleTheme: () => void;
  onLoggedIn: () => void;
}) {
  const [username, setUsername] = useState(status.username);
  const [password, setPassword] = useState("");
  const login = useMutation({
    mutationFn: () => api.login(username.trim(), password),
    onSuccess: () => {
      setPassword("");
      onLoggedIn();
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (username.trim() && password) login.mutate();
  };

  return <AuthFrame dark={dark} onToggleTheme={onToggleTheme}>
    <div className="auth-state-icon"><LockKeyhole size={25} /></div>
    <span className="auth-eyebrow">Local admin account</span>
    <h1>Sign in to Codebridge</h1>
    <p>The Admin API is protected by a local account and an HttpOnly browser session.</p>
    {login.error && <Notice tone="danger">{errorMessage(login.error)}</Notice>}
    <form className="auth-form" onSubmit={submit}>
      <label className="field">
        <span className="field-label">Username</span>
        <TextInput value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" autoFocus />
      </label>
      <label className="field">
        <span className="field-label">Password</span>
        <TextInput type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete="current-password" />
      </label>
      <Button type="submit" loading={login.isPending} disabled={!username.trim() || !password}><LogIn size={15} /> Sign in</Button>
    </form>
    <div className="auth-cli-note"><Terminal size={17} /><div><strong>Forgot the password?</strong><span>Reset it only from the local terminal:</span><code>codebridge admin reset-password {status.username || "admin"}</code></div></div>
  </AuthFrame>;
}

function SetupRequired({ status, dark, onToggleTheme, onConfigured }: {
  status: AdminAuthStatus;
  dark: boolean;
  onToggleTheme: () => void;
  onConfigured: () => void;
}) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const setup = useMutation({
    mutationFn: () => api.setupAdmin(username.trim(), password),
    onSuccess: () => {
      setPassword("");
      setConfirmation("");
      onConfigured();
    },
  });
  const mismatch = !!confirmation && password !== confirmation;
  const valid = !!username.trim() && password.length >= 8 && password === confirmation;
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (valid) setup.mutate();
  };

  return <AuthFrame dark={dark} onToggleTheme={onToggleTheme}>
    <div className="auth-state-icon warning"><ShieldCheck size={25} /></div>
    <span className="auth-eyebrow">One-time local setup</span>
    <h1>Create the admin account</h1>
    <p>This create-only endpoint is available only from the loopback Admin UI while no credential file exists. Password recovery and reset remain CLI-only.</p>
    {setup.error && <Notice tone="danger">{errorMessage(setup.error)}</Notice>}
    <form className="auth-form" onSubmit={submit}>
      <label className="field">
        <span className="field-label">Username</span>
        <TextInput value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" />
      </label>
      <label className="field">
        <span className="field-label">Admin password</span>
        <TextInput type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete="new-password" autoFocus />
        <span className="field-hint">At least 8 characters.</span>
      </label>
      <label className="field">
        <span className="field-label">Confirm password</span>
        <TextInput type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="new-password" />
        {mismatch && <span className="field-hint auth-field-error">Passwords do not match.</span>}
      </label>
      <Button type="submit" loading={setup.isPending} disabled={!valid}><ShieldCheck size={15} /> Create account and continue</Button>
    </form>
    <p className="auth-path">Credential file: <code>{status.credentialPath}</code></p>
    <div className="auth-cli-note"><Terminal size={17} /><div><strong>Prefer the terminal?</strong><span>You can still create the first account locally:</span><code>codebridge admin set-password {username.trim() || "admin"}</code></div></div>
  </AuthFrame>;
}

function AuthFrame({ children, dark, onToggleTheme }: { children: ReactNode; dark: boolean; onToggleTheme: () => void }) {
  return <main className="auth-page">
    <button className="auth-theme-button" onClick={onToggleTheme} aria-label="Toggle color theme">{dark ? <Sun size={17} /> : <Moon size={17} />}</button>
    <section className="auth-card">
      <div className="auth-brand"><span className="brand-mark"><Code2 size={22} /></span><span><strong>Codebridge</strong><small>Admin Console</small></span></div>
      <div className="auth-content">{children}</div>
    </section>
  </main>;
}

function AuthLoading() {
  return <main className="auth-page"><div className="auth-loading"><LoaderCircle size={28} className="spin" /><span>Checking local admin session…</span></div></main>;
}

function errorMessage(error: unknown): string {
  return error instanceof APIError ? error.message : error instanceof Error ? error.message : error ? String(error) : "";
}
