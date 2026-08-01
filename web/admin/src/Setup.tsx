import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  CheckCircle2,
  Database,
  ExternalLink,
  KeyRound,
  Network,
  Save,
  Settings2,
  ShieldCheck,
} from "lucide-react";
import { api, APIError } from "./api";
import { Badge, Button, Card, Field, LoadingPage, Notice, PageHeader, Select, TextInput, Toggle } from "./components";
import type { CodebridgeConfig } from "./types";

const OPENAI_TUNNELS_URL = "https://platform.openai.com/settings/organization/tunnels";
const OPENAI_API_KEYS_URL = "https://platform.openai.com/settings/organization/api-keys";

type StepID = "runtime" | "tunnel" | "api-key" | "memory" | "review";

const steps: { id: StepID; label: string; description: string; icon: React.ReactNode }[] = [
  { id: "runtime", label: "Runtime", description: "Workspace and safety", icon: <Settings2 size={17} /> },
  { id: "tunnel", label: "Tunnel", description: "Secure MCP access", icon: <Network size={17} /> },
  { id: "api-key", label: "API key", description: "Write-only runtime key", icon: <KeyRound size={17} /> },
  { id: "memory", label: "Memory", description: "Optional provider", icon: <Database size={17} /> },
  { id: "review", label: "Review", description: "Validate and save", icon: <CheckCircle2 size={17} /> },
];

export function Setup() {
  const queryClient = useQueryClient();
  const snapshot = useQuery({ queryKey: ["config"], queryFn: api.config });
  const secrets = useQuery({ queryKey: ["secrets"], queryFn: api.secrets });
  const [draft, setDraft] = useState<CodebridgeConfig | null>(null);
  const [step, setStep] = useState<StepID>("runtime");
  const [apiKey, setAPIKey] = useState("");
  const [memorySecret, setMemorySecret] = useState("");
  const [message, setMessage] = useState<{ tone: "success" | "danger" | "info"; text: string } | null>(null);
  const tunnelInput = useRef<HTMLInputElement>(null);
  const apiKeyInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (snapshot.data && !draft) setDraft(structuredClone(snapshot.data.config));
  }, [snapshot.data, draft]);

  const save = useMutation({
    mutationFn: async () => {
      const prepared: CodebridgeConfig = {
        ...draft!,
        workspace: draft!.workspace.trim(),
        tunnelId: draft!.tunnelId?.trim(),
        tunnelBin: draft!.tunnelBin?.trim(),
        runtimeKeyEnv: draft!.runtimeKeyEnv?.trim(),
        memory: {
          ...draft!.memory,
          provider: draft!.memory.provider.trim(),
          endpoint: draft!.memory.endpoint?.trim(),
          secretEnv: draft!.memory.secretEnv?.trim(),
        },
      };
      const validated = await api.validateConfig(prepared);
      const saved = await api.saveConfig(validated.config, snapshot.data!.revision);
      const secretErrors: string[] = [];
      let runtimeKeyStored = false;
      let memorySecretStored = false;

      if (!saved.config.noTunnel && apiKey && saved.config.runtimeKeyEnv) {
        try {
          await api.setSecret(saved.config.runtimeKeyEnv, apiKey.trim());
          runtimeKeyStored = true;
        } catch (error) {
          secretErrors.push(`${saved.config.runtimeKeyEnv}: ${errorMessage(error)}`);
        }
      }
      if (saved.config.memory.enabled && memorySecret && saved.config.memory.secretEnv) {
        try {
          await api.setSecret(saved.config.memory.secretEnv, memorySecret);
          memorySecretStored = true;
        } catch (error) {
          secretErrors.push(`${saved.config.memory.secretEnv}: ${errorMessage(error)}`);
        }
      }
      return { saved, secretErrors, runtimeKeyStored, memorySecretStored };
    },
    onSuccess: ({ saved, secretErrors, runtimeKeyStored, memorySecretStored }) => {
      queryClient.setQueryData(["config"], saved);
      setDraft(structuredClone(saved.config));
      if (runtimeKeyStored) setAPIKey("");
      if (memorySecretStored) setMemorySecret("");
      void queryClient.invalidateQueries({ queryKey: ["secrets"] });
      void queryClient.invalidateQueries({ queryKey: ["bootstrap"] });
      void queryClient.invalidateQueries({ queryKey: ["workspaces"] });
      setMessage(secretErrors.length
        ? { tone: "danger", text: `Configuration was saved, but these secret writes failed: ${secretErrors.join("; ")}` }
        : { tone: "success", text: "Setup was saved safely. Restart Codebridge to activate the new runtime and tunnel settings." });
      setStep("review");
    },
    onError: (error) => setMessage({ tone: "danger", text: errorMessage(error) }),
  });

  if (snapshot.isLoading || !snapshot.data || !draft) return <LoadingPage />;

  const activeIndex = steps.findIndex((item) => item.id === step);
  const runtimeSecret = secrets.data?.secrets.find((item) => item.name === draft.runtimeKeyEnv);
  const memorySecretState = secrets.data?.secrets.find((item) => item.name === draft.memory.secretEnv);
  const runtimeReady = !!draft.workspace.trim() && draft.port > 0 && draft.port <= 65535;
  const tunnelReady = !!draft.noTunnel || !!draft.tunnelId?.trim();
  const apiKeyReady = !!draft.noTunnel || (!!draft.runtimeKeyEnv?.trim() && (!!runtimeSecret?.configured || !!apiKey.trim()));
  const overallReady = runtimeReady && tunnelReady && apiKeyReady;
  const stepReady = step === "runtime" ? runtimeReady : step === "tunnel" ? tunnelReady : step === "api-key" ? apiKeyReady : true;

  const update = <K extends keyof CodebridgeConfig>(key: K, value: CodebridgeConfig[K]) => {
    setDraft({ ...draft, [key]: value });
    setMessage(null);
  };
  const updateMemory = (patch: Partial<CodebridgeConfig["memory"]>) => {
    setDraft({ ...draft, memory: { ...draft.memory, ...patch } });
    setMessage(null);
  };
  const openExternal = (url: string, input: HTMLInputElement | null) => {
    input?.focus();
    window.open(url, "_blank", "noopener,noreferrer");
    window.setTimeout(() => input?.focus(), 150);
  };
  const previous = () => activeIndex > 0 && setStep(steps[activeIndex - 1].id);
  const next = () => activeIndex < steps.length - 1 && stepReady && setStep(steps[activeIndex + 1].id);

  return <>
    <PageHeader
      eyebrow="Guided first-run flow"
      title="Setup Codebridge"
      description="Configure the same core choices as codebridge setup while preserving existing advanced settings and keeping secret values out of config.json."
      actions={<Badge tone="info">Step {activeIndex + 1} of {steps.length}</Badge>}
    />
    {message && <Notice tone={message.tone}>{message.text}</Notice>}
    <div className="setup-layout">
      <nav className="setup-stepper" aria-label="Setup steps">
        {steps.map((item, index) => <button
          key={item.id}
          className={`${step === item.id ? "active" : ""} ${index < activeIndex ? "complete" : ""}`}
          onClick={() => setStep(item.id)}
        >
          <span className="setup-step-icon">{index < activeIndex ? <CheckCircle2 size={17} /> : item.icon}</span>
          <span><strong>{item.label}</strong><small>{item.description}</small></span>
        </button>)}
      </nav>

      <div className="setup-content">
        {step === "runtime" && <Card title="Runtime and access" description="Choose the primary workspace and the same safety controls prompted by the CLI setup command.">
          <div className="form-grid">
            <Field label="Primary workspace" hint="Must be an existing local directory." wide>
              <TextInput value={draft.workspace} onChange={(event) => update("workspace", event.target.value)} autoFocus />
            </Field>
            <Field label="Execution mode">
              <Select value={draft.mode} onChange={(event) => update("mode", event.target.value)}><option value="safe">safe</option><option value="full">full</option></Select>
            </Field>
            <Field label="Policy">
              <Select value={draft.policy} onChange={(event) => update("policy", event.target.value)}><option value="strict">strict</option><option value="balanced">balanced</option><option value="full">full</option></Select>
            </Field>
            <Field label="MCP port">
              <TextInput type="number" min={1} max={65535} value={draft.port} onChange={(event) => update("port", Number.parseInt(event.target.value || "0", 10))} />
            </Field>
          </div>
        </Card>}

        {step === "tunnel" && <Card title="Secure MCP Tunnel" description="Create the tunnel in OpenAI Platform, then paste its ID into Codebridge.">
          <div className="toggle-stack">
            <Toggle checked={!draft.noTunnel} onChange={(enabled) => update("noTunnel", !enabled)} label="Use ChatGPT Web tunnel" description="Disable this to keep Codebridge local-only." />
          </div>
          {!draft.noTunnel ? <>
            <ExternalSetupCard
              icon={<Network size={21} />}
              title="Create or inspect a tunnel"
              description="Open the OpenAI organization tunnel settings in a new browser tab. Codebridge cannot read that page, so copy the generated tunnel ID back here."
              button="Open tunnel settings"
              onOpen={() => openExternal(OPENAI_TUNNELS_URL, tunnelInput.current)}
            />
            <div className="form-grid top-gap">
              <Field label="Tunnel ID" hint="Paste the tunnel_… identifier from OpenAI Platform." wide>
                <input ref={tunnelInput} className="control" value={draft.tunnelId ?? ""} onChange={(event) => update("tunnelId", event.target.value)} placeholder="tunnel_…" />
              </Field>
              <Field label="Tunnel client path" hint="Keep the detected/default path unless tunnel-client is installed elsewhere." wide>
                <TextInput value={draft.tunnelBin ?? ""} onChange={(event) => update("tunnelBin", event.target.value)} />
              </Field>
            </div>
          </> : <Notice tone="info">Tunnel setup is skipped. The MCP endpoint will remain local to this machine.</Notice>}
        </Card>}

        {step === "api-key" && <Card title="Runtime API key" description="The value is written only to the owner-only Codebridge .env file and is never returned by the API.">
          {draft.noTunnel ? <Notice tone="info">A runtime API key is not required while the tunnel is disabled. You can continue to the next step.</Notice> : <>
            <ExternalSetupCard
              icon={<KeyRound size={21} />}
              title="Create an OpenAI API key"
              description="Open the organization API key settings in a new tab, create a key, then paste it into the focused write-only field below. Browser cross-origin rules prevent Codebridge from auto-reading or auto-filling the OpenAI page."
              button="Open API key settings"
              onOpen={() => openExternal(OPENAI_API_KEYS_URL, apiKeyInput.current)}
            />
            <div className="form-grid top-gap">
              <Field label="Runtime key environment" hint="The variable name stored in config.json; the value stays in .env." wide>
                <TextInput value={draft.runtimeKeyEnv ?? ""} onChange={(event) => update("runtimeKeyEnv", event.target.value)} placeholder="CONTROL_PLANE_API_KEY" />
              </Field>
              <Field label="New API key" hint={runtimeSecret?.configured ? "A value is already configured. Leave this empty to keep it." : "Required because no value is currently configured."} wide>
                <input ref={apiKeyInput} className="control" type="password" autoComplete="new-password" value={apiKey} onChange={(event) => setAPIKey(event.target.value)} placeholder={runtimeSecret?.configured ? "Leave empty to keep existing value" : "Paste the generated API key"} />
              </Field>
            </div>
            <div className="setup-secret-state"><ShieldCheck size={18} /><span><strong>{runtimeSecret?.configured ? "Runtime key already configured" : "Runtime key still required"}</strong><small>{runtimeSecret?.configured ? `Source: ${runtimeSecret.source}` : "The value will be stored only after the final save."}</small></span><Badge tone={runtimeSecret?.configured ? "success" : "warning"}>{runtimeSecret?.configured ? "Protected" : "Missing"}</Badge></div>
          </>}
        </Card>}

        {step === "memory" && <Card title="Project memory" description="This optional step mirrors the core memory questions from codebridge setup; advanced delivery settings remain unchanged.">
          <div className="toggle-stack">
            <Toggle checked={draft.memory.enabled} onChange={(enabled) => updateMemory({ enabled })} label="Enable memory" description="Capture selected project context for later retrieval." />
          </div>
          {draft.memory.enabled && <div className="form-grid top-gap">
            <Field label="Provider"><TextInput value={draft.memory.provider} onChange={(event) => updateMemory({ provider: event.target.value })} placeholder="agentmemory" /></Field>
            <Field label="Endpoint"><TextInput value={draft.memory.endpoint ?? ""} onChange={(event) => updateMemory({ endpoint: event.target.value })} /></Field>
            <Field label="Secret environment" hint="Leave empty when the provider does not require authentication." wide><TextInput value={draft.memory.secretEnv ?? ""} onChange={(event) => updateMemory({ secretEnv: event.target.value })} /></Field>
            {!!draft.memory.secretEnv?.trim() && <Field label="New memory secret" hint={memorySecretState?.configured ? "Leave empty to keep the existing value." : "Optional unless required by your provider."} wide>
              <TextInput type="password" autoComplete="new-password" value={memorySecret} onChange={(event) => setMemorySecret(event.target.value)} placeholder={memorySecretState?.configured ? "Leave empty to keep existing value" : "Enter provider secret"} />
            </Field>}
          </div>}
        </Card>}

        {step === "review" && <Card title="Review and save" description="The server will validate the complete configuration and use its current revision to avoid overwriting concurrent changes.">
          <div className="setup-review-grid">
            <ReviewItem label="Workspace" value={draft.workspace} />
            <ReviewItem label="Mode / policy" value={`${draft.mode} / ${draft.policy}`} />
            <ReviewItem label="MCP listener" value={`${draft.host}:${draft.port}`} />
            <ReviewItem label="Tunnel" value={draft.noTunnel ? "Disabled (local only)" : draft.tunnelId || "Tunnel ID missing"} />
            <ReviewItem label="Runtime key" value={draft.noTunnel ? "Not required" : apiKey ? "New write-only value ready" : runtimeSecret?.configured ? "Existing value retained" : "Missing"} />
            <ReviewItem label="Memory" value={draft.memory.enabled ? `${draft.memory.provider}${draft.memory.endpoint ? ` · ${draft.memory.endpoint}` : ""}` : "Disabled"} />
          </div>
          {!overallReady && <Notice tone="danger">Complete the required runtime, tunnel, and API-key fields before saving.</Notice>}
          <Notice tone="warning"><strong>Restart required.</strong> Saving updates files atomically, but the active daemon and tunnel continue using their current settings until Codebridge is restarted.</Notice>
        </Card>}

        <div className="setup-actions">
          <Button variant="secondary" onClick={previous} disabled={activeIndex === 0 || save.isPending}><ArrowLeft size={15} /> Previous</Button>
          <span>{!stepReady && step === "tunnel" ? "Enter a tunnel ID or disable the tunnel." : !stepReady && step === "api-key" ? "Provide a runtime key or keep an existing configured value." : ""}</span>
          {step !== "review"
            ? <Button onClick={next} disabled={!stepReady || save.isPending}>Next <ArrowRight size={15} /></Button>
            : <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!overallReady}><Save size={15} /> Save setup</Button>}
        </div>
      </div>
    </div>
  </>;
}

function ExternalSetupCard({ icon, title, description, button, onOpen }: {
  icon: React.ReactNode;
  title: string;
  description: string;
  button: string;
  onOpen: () => void;
}) {
  return <div className="setup-external-card">
    <span>{icon}</span>
    <div><strong>{title}</strong><small>{description}</small></div>
    <Button variant="secondary" onClick={onOpen}><ExternalLink size={15} /> {button}</Button>
  </div>;
}

function ReviewItem({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><strong>{value || "Not configured"}</strong></div>;
}

const errorMessage = (error: unknown) => error instanceof APIError ? error.message : error instanceof Error ? error.message : String(error);
