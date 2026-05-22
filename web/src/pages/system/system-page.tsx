import { useState, useRef } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { changePassword, exportSystemData, importSystemData, confirmImport, ImportValidationResult, getSystemVersion } from "@/services/system";
import { checkHealth, checkReady } from "@/services/health";
import { listAgents } from "@/services/agents";
import { downloadDiagnosticBundle } from "@/services/diagnostic";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle, CardFooter } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { ErrorPanel } from "@/components/error-panel";
import { Download, ShieldCheck, CheckCircle2, Activity, Server, Database, KeyRound, FolderTree, AlertCircle, RefreshCw, Upload, ExternalLink, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { StatusBadge } from "@/components/status-badge";
import { ConfirmDialog } from "@/components/confirm-dialog";
import type { Agent } from "@/types/agent";

export function SystemPage() {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordSuccess, setPasswordSuccess] = useState(false);
  const [importResult, setImportResult] = useState<ImportValidationResult | null>(null);
  const [showImportConfirm, setShowImportConfirm] = useState(false);
  const [selectedAgents, setSelectedAgents] = useState<string[]>([]);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const { data: healthStatus, refetch: refetchHealth, isFetching: healthFetching } = useQuery({
    queryKey: ["health"],
    queryFn: checkHealth,
    refetchInterval: 30000,
  });

  const { data: readyStatus, refetch: refetchReady, isFetching: readyFetching } = useQuery({
    queryKey: ["ready"],
    queryFn: checkReady,
    refetchInterval: 30000,
  });

  const { data: versionInfo } = useQuery({
    queryKey: ["system-version"],
    queryFn: getSystemVersion,
  });

  const { data: agents = [] } = useQuery({
    queryKey: ["agents"],
    queryFn: listAgents,
  });

  const passwordMutation = useMutation({
    mutationFn: changePassword,
    onSuccess: () => {
      setPasswordSuccess(true);
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      toast.success("密码修改成功");
      setTimeout(() => setPasswordSuccess(false), 3000);
    },
    onError: (error: any) => {
      toast.error("修改密码失败", { description: error.message });
    }
  });

  const exportMutation = useMutation({
    mutationFn: exportSystemData,
    onSuccess: (blob) => {
      const url = window.URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `vaultfleet-export-${new Date().toISOString().split("T")[0]}.zip`;
      document.body.appendChild(a);
      a.click();
      window.URL.revokeObjectURL(url);
      toast.success("数据导出成功");
    },
    onError: (error: any) => {
      toast.error("数据导出失败", { description: error.message });
    }
  });

  const importMutation = useMutation({
    mutationFn: importSystemData,
    onSuccess: (result) => {
      if (result.valid) {
        setImportResult(result);
        setShowImportConfirm(true);
      } else {
        setImportResult(result);
        toast.error("备份文件验证失败", {
          description: result.errors.join("；"),
        });
      }
    },
    onError: (error: any) => {
      toast.error("上传失败", { description: error.message });
    },
  });

  const confirmMutation = useMutation({
    mutationFn: confirmImport,
    onSuccess: () => {
      setShowImportConfirm(false);
      toast.success("导入已确认，Master 正在重启...");
      const poll = setInterval(async () => {
        try {
          const res = await fetch("/health");
          if (res.ok) {
            clearInterval(poll);
            window.location.reload();
          }
        } catch {}
      }, 2000);
    },
    onError: (error: any) => {
      toast.error("确认导入失败", { description: error.message });
    },
  });

  const diagnosticMutation = useMutation({
    mutationFn: downloadDiagnosticBundle,
    onSuccess: (blob) => {
      const url = window.URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `vaultfleet-diagnostic-${new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19)}.zip`;
      document.body.appendChild(a);
      a.click();
      window.URL.revokeObjectURL(url);
      document.body.removeChild(a);
      toast.success("诊断包已生成");
    },
    onError: (error: any) => {
      toast.error("生成诊断包失败", { description: error.message });
    },
  });

  const handleImportClick = () => {
    fileInputRef.current?.click();
  };

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) {
      importMutation.mutate(file);
      e.target.value = "";
    }
  };

  const handlePasswordSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (newPassword !== confirmPassword) {
      toast.error("新密码不匹配");
      return;
    }
    passwordMutation.mutate({ current_password: currentPassword, new_password: newPassword });
  };

  const toggleAgent = (id: string) => {
    setSelectedAgents((prev) => (
      prev.includes(id) ? prev.filter((agentID) => agentID !== id) : [...prev, id]
    ));
  };

  const isRefreshing = healthFetching || readyFetching;

  return (
    <div className="space-y-6 max-w-4xl">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">系统管理</h1>
        <Button 
          variant="outline" 
          size="sm" 
          onClick={() => { refetchHealth(); refetchReady(); }}
          disabled={isRefreshing}
        >
          <RefreshCw className={isRefreshing ? "h-4 w-4 mr-2 animate-spin" : "h-4 w-4 mr-2"} />
          刷新状态
        </Button>
      </div>

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="text-lg flex items-center gap-2">
              <Activity className="h-5 w-5 text-primary" />
              系统状态
            </CardTitle>
            {versionInfo?.version && (
              <span className="text-xs font-mono text-muted-foreground bg-muted px-2 py-1 rounded">
                {versionInfo.version}
              </span>
            )}
          </div>
          <CardDescription>Master 服务运行及依赖组件就绪状态。</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-6 md:grid-cols-2">
          <div className="space-y-4">
            <div className="flex items-center justify-between p-3 border rounded-lg">
              <div className="flex items-center gap-3">
                <Server className="h-5 w-5 text-muted-foreground" />
                <div>
                  <div className="text-sm font-medium">服务进程</div>
                  <div className="text-xs text-muted-foreground">HTTP API Server</div>
                </div>
              </div>
              <StatusBadge status={healthStatus?.ok ? "success" : "failed"} />
            </div>

            <div className="flex items-center justify-between p-3 border rounded-lg">
              <div className="flex items-center gap-3">
                <Database className="h-5 w-5 text-muted-foreground" />
                <div>
                  <div className="text-sm font-medium">数据库连接</div>
                  <div className="text-xs text-muted-foreground">SQLite 存储</div>
                </div>
              </div>
              <StatusBadge status={readyStatus?.ok || readyStatus?.status === "ready" ? "success" : "failed"} />
            </div>
          </div>

          <div className="space-y-4">
            <div className="flex items-center justify-between p-3 border rounded-lg">
              <div className="flex items-center gap-3">
                <KeyRound className="h-5 w-5 text-muted-foreground" />
                <div>
                  <div className="text-sm font-medium">Master Key</div>
                  <div className="text-xs text-muted-foreground">数据加密密钥</div>
                </div>
              </div>
              <StatusBadge status={readyStatus?.ok ? "success" : "failed"} />
            </div>

            <div className="flex items-center justify-between p-3 border rounded-lg">
              <div className="flex items-center gap-3">
                <FolderTree className="h-5 w-5 text-muted-foreground" />
                <div>
                  <div className="text-sm font-medium">数据目录</div>
                  <div className="text-xs text-muted-foreground">本地存储可用性</div>
                </div>
              </div>
              <StatusBadge status={readyStatus?.ok ? "success" : "failed"} />
            </div>
          </div>
          
          {!readyStatus?.ok && readyStatus?.error && (
            <div className="md:col-span-2 flex items-start gap-2 text-red-600 bg-red-50 p-3 rounded border border-red-200 text-xs">
              <AlertCircle className="h-4 w-4 shrink-0 mt-0.5" />
              <div>
                <span className="font-bold mr-1">系统未就绪:</span>
                {readyStatus.error}
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>修改密码</CardTitle>
            <CardDescription>定期修改密码以确保账户安全。</CardDescription>
          </CardHeader>
          <form onSubmit={handlePasswordSubmit}>
            <CardContent className="space-y-4">
              <ErrorPanel error={passwordMutation.error as any} />
              {passwordSuccess && (
                <div className="flex items-center gap-2 text-green-600 text-sm font-medium bg-green-50 p-3 rounded border border-green-200">
                  <CheckCircle2 className="h-4 w-4" /> 密码修改成功
                </div>
              )}
              <div className="space-y-2">
                <Label htmlFor="current">当前密码</Label>
                <Input
                  id="current"
                  type="password"
                  value={currentPassword}
                  onChange={(e) => setCurrentPassword(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="new">新密码</Label>
                <Input
                  id="new"
                  type="password"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="confirm">确认新密码</Label>
                <Input
                  id="confirm"
                  type="password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  required
                />
              </div>
            </CardContent>
            <CardFooter>
              <Button type="submit" disabled={passwordMutation.isPending}>
                {passwordMutation.isPending ? "正在修改..." : "提交修改"}
              </Button>
            </CardFooter>
          </form>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>数据管理</CardTitle>
            <CardDescription>导出或导入 Master 节点的完整数据。建议在进行系统迁移或重大更新前导出备份。</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex flex-col items-center justify-center py-8 text-center space-y-4 bg-muted/20 rounded-lg border-2 border-dashed">
              <ShieldCheck className="h-12 w-12 text-muted-foreground opacity-30" />
              <p className="text-sm text-muted-foreground px-6">
                导出的压缩包包含 SQLite 数据库和加密密钥。请务必加密存储导出的文件。
              </p>
            </div>
            {importResult && !importResult.valid && (
              <div className="text-xs text-red-600 bg-red-50 p-3 rounded border border-red-200">
                <div className="font-bold mb-1">验证失败：</div>
                <ul className="list-disc list-inside">
                  {importResult.errors.map((err, i) => (
                    <li key={i}>{err}</li>
                  ))}
                </ul>
              </div>
            )}
          </CardContent>
          <CardFooter className="flex gap-2">
            <Button
              variant="outline"
              className="flex-1"
              onClick={() => exportMutation.mutate()}
              disabled={exportMutation.isPending}
            >
              <Download className="mr-2 h-4 w-4" />
              {exportMutation.isPending ? "正在导出..." : "导出数据"}
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              accept=".zip"
              className="hidden"
              onChange={handleFileChange}
            />
            <Button
              variant="outline"
              className="flex-1"
              onClick={handleImportClick}
              disabled={importMutation.isPending}
            >
              <Upload className="mr-2 h-4 w-4" />
              {importMutation.isPending ? "正在验证..." : "导入数据"}
            </Button>
          </CardFooter>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-lg">诊断包</CardTitle>
          <CardDescription>收集系统状态和日志，用于问题排查。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {agents.length > 0 && (
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">选择需要收集日志的 Agent（可选）：</p>
              {agents.map((agent: Agent) => (
                <label key={agent.id} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={selectedAgents.includes(agent.id)}
                    onCheckedChange={() => toggleAgent(agent.id)}
                    disabled={agent.status !== "online" || diagnosticMutation.isPending}
                  />
                  <span className={agent.status !== "online" ? "text-muted-foreground" : ""}>
                    {agent.name}
                  </span>
                  {agent.status !== "online" && (
                    <span className="text-xs text-muted-foreground">（离线）</span>
                  )}
                </label>
              ))}
            </div>
          )}
        </CardContent>
        <CardFooter>
          <Button
            variant="outline"
            onClick={() => diagnosticMutation.mutate(selectedAgents)}
            disabled={diagnosticMutation.isPending}
          >
            {diagnosticMutation.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                正在生成...
              </>
            ) : (
              <>
                <Download className="mr-2 h-4 w-4" />
                生成诊断包
              </>
            )}
          </Button>
        </CardFooter>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-lg">问题反馈</CardTitle>
          <CardDescription>遇到 Bug 或有建议？提交 Issue 到 GitHub。</CardDescription>
        </CardHeader>
        <CardFooter>
          <Button
            variant="outline"
            onClick={() => {
              const params = new URLSearchParams({
                template: "bug_report.yml",
                version: versionInfo?.version || "unknown",
              });
              window.open(
                `https://github.com/momo-z/VaultFleet/issues/new?${params.toString()}`,
                "_blank"
              );
            }}
          >
            <ExternalLink className="mr-2 h-4 w-4" />
            提交 Issue
          </Button>
        </CardFooter>
      </Card>

      <ConfirmDialog
        open={showImportConfirm}
        onOpenChange={(open) => !open && setShowImportConfirm(false)}
        title="确认导入备份数据"
        description="导入将替换当前所有 Master 数据，Master 将自动重启。当前数据会保存到 rollback 目录。此操作不可撤销，是否继续？"
        confirmText="确认导入并重启"
        onConfirm={() => confirmMutation.mutate()}
        loading={confirmMutation.isPending}
      />
    </div>
  );
}
