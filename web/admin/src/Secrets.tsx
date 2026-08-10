import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { EyeOff, KeyRound, Save, Trash2 } from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Button, Card, EmptyState, LoadingPage, Notice, PageHeader, TextInput } from "./components";

export function Secrets() {
  const queryClient = useQueryClient();
  const secrets = useQuery({ queryKey: ["secrets"], queryFn: api.secrets });
  const [selected, setSelected] = useState("");
  const [value, setValue] = useState("");
  const [message, setMessage] = useState<{ tone: "success" | "danger"; text: string } | null>(null);
  const items = secrets.data?.secrets ?? [];
  const current = items.find((item) => item.name === selected) ?? items[0];

  const setSecret = useMutation({
    mutationFn: () => api.setSecret(current.name, value),
    onSuccess: () => { setValue(""); setMessage({ tone: "success", text: `${current.name} was stored without exposing its value.` }); void queryClient.invalidateQueries({ queryKey: ["secrets"] }); },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });
  const removeSecret = useMutation({
    mutationFn: () => api.deleteSecret(current.name),
    onSuccess: () => { setValue(""); setMessage({ tone: "success", text: `${current.name} was removed.` }); void queryClient.invalidateQueries({ queryKey: ["secrets"] }); },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  if (secrets.isLoading) return <LoadingPage />;

  return <>
    <PageHeader eyebrow="Write-only values" title="Secrets" description="Manage only environment variables referenced by the current configuration." />
    <Notice tone="info"><strong>Values are never returned.</strong> The server reports presence only, writes the owner-only .env file atomically and requires a restart for dependent services.</Notice>
    <div className="secret-layout">
      <Card title="Referenced variables" description={secrets.data?.path}>
        <div className="secret-list">
          {items.map((item) => <button key={item.name} className={(current?.name === item.name) ? "active" : ""} onClick={() => { setSelected(item.name); setValue(""); setMessage(null); }}><KeyRound size={17} /><span><strong>{item.name}</strong><small>{item.referencedBy.length} reference{item.referencedBy.length === 1 ? "" : "s"}</small></span><Badge tone={item.managed ? "success" : item.configured ? "info" : "warning"}>{item.managed ? "Managed" : item.configured ? "External" : "Missing"}</Badge></button>)}
          {!items.length && <EmptyState title="No referenced secrets" description="Set memory.secretEnv, runtimeKeyEnv, remote ingress token refs, envRefs or headerRefs in configuration first." />}
        </div>
      </Card>
      <div>{current && <Card title={current.name} description="Entering a new value replaces the existing value; it cannot be viewed again.">
        {message && <Notice tone={message.tone}>{message.text}</Notice>}
        <div className="secret-status"><EyeOff size={20} /><div><span>Current state</span><strong>{current.managed ? "Managed by Wormhole .env" : current.configured ? "Provided by process environment" : "Not configured"}</strong></div><Badge tone={current.managed ? "success" : current.configured ? "info" : "warning"}>{current.managed ? "Protected" : current.configured ? "External" : "Action needed"}</Badge></div>
        <label className="field top-gap"><span className="field-label">New secret value</span><TextInput type="password" autoComplete="new-password" value={value} onChange={(e) => setValue(e.target.value)} placeholder="Enter a new value" /></label>
        <div className="button-row"><Button onClick={() => setSecret.mutate()} loading={setSecret.isPending} disabled={!value}><Save size={15} /> Store value</Button><Button variant="danger" onClick={() => { if (window.confirm(`Delete the managed value for ${current.name}?`)) removeSecret.mutate(); }} loading={removeSecret.isPending} disabled={!current.managed}><Trash2 size={15} /> Delete managed value</Button></div>
        <div className="reference-list"><span>Referenced by</span>{current.referencedBy.map((reference) => <code key={reference}>{reference}</code>)}</div>
      </Card>}</div>
    </div>
  </>;
}

const errorMessage = (error: unknown) => error instanceof APIError ? error.message : error instanceof Error ? error.message : String(error);
