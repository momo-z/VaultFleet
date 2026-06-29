import { Agent, ApiAgent, CreateAgentResponse } from "@/types/agent";
import { BrowseRequest, BrowseResponse, DirSizeRequest, DirSizeResponse } from "@/types/api";
import { MaintenanceOperation } from "@/types/maintenance";
import { apiDelete, apiGet, apiPost } from "./http";

export const listAgents = async () => (await apiGet<ApiAgent[]>("/api/agents")).map(normalizeAgent);
export const createAgent = (body: { name: string }) => apiPost<CreateAgentResponse>("/api/agents", body);
export const getAgent = async (id: string) => normalizeAgent(await apiGet<ApiAgent>(`/api/agents/${id}`));
export const deleteAgent = (id: string) => apiDelete(`/api/agents/${id}`);
export const regenerateAgentToken = (id: string) => apiPost<CreateAgentResponse>(`/api/agents/${id}/regenerate-token`);
export const getInstallToken = (id: string) => apiGet<{ id: string; enroll_token: string; enrolled: boolean }>(`/api/agents/${id}/install-token`);
export const browseAgent = (id: string, body: BrowseRequest) => apiPost<BrowseResponse>(`/api/agents/${id}/browse`, body);
export const dirSizeAgent = (id: string, body: DirSizeRequest) => apiPost<DirSizeResponse>(`/api/agents/${id}/dir-size`, body);
export const backupNow = (id: string) => apiPost<{ command_id: string; message_id: string }>(`/api/agents/${id}/backup-now`);
export const triggerMaintenance = (id: string, operation: MaintenanceOperation) => apiPost<{ command_id: string; message_id: string }>(`/api/agents/${id}/maintenance`, { operation });

export function normalizeAgent(agent: ApiAgent): Agent {
  const systemInfo = parseSystemInfo(agent.system_info);
  return {
    id: agent.id,
    name: agent.name,
    status: agent.status,
    last_seen: agent.last_seen ?? agent.last_seen_at ?? "",
    version: agent.version ?? systemInfo.version ?? "",
    hostname: agent.hostname ?? systemInfo.hostname ?? "",
    os: agent.os ?? systemInfo.os ?? "",
    arch: agent.arch ?? systemInfo.arch ?? "",
    created_at: agent.created_at,
  };
}

function parseSystemInfo(raw: string | null | undefined): Partial<Pick<Agent, "version" | "hostname" | "os" | "arch">> {
  if (!raw) {
    return {};
  }
  try {
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") {
      return {};
    }
    return {
      version: stringField(parsed, "version"),
      hostname: stringField(parsed, "hostname"),
      os: stringField(parsed, "os"),
      arch: stringField(parsed, "arch"),
    };
  } catch {
    return {};
  }
}

function stringField(value: object, key: string): string | undefined {
  const field = (value as Record<string, unknown>)[key];
  return typeof field === "string" ? field : undefined;
}
