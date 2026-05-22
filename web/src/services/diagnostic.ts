import { ApiError } from "./http";

export async function downloadDiagnosticBundle(agentIds: string[]): Promise<Blob> {
  const params = agentIds.length > 0 ? `?agents=${agentIds.join(",")}` : "";
  const response = await fetch(`/api/system/diagnostic${params}`, {
    credentials: "same-origin",
  });
  if (!response.ok) {
    throw new ApiError("diagnostic bundle failed", response.status, await response.text());
  }
  return response.blob();
}
