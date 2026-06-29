import React, { useState } from "react";
import { useParams, Link } from "react-router-dom";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { getAgent, backupNow, triggerMaintenance } from "@/services/agents";
import { listPolicies } from "@/services/policies";
import { copyToClipboard } from "@/lib/utils";
import { listTasks } from "@/services/tasks";
import { listSnapshots, restoreSnapshot } from "@/services/snapshots";
import { listAgentCommands } from "@/services/commands";
import { StatusBadge } from "@/components/status-badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { DirectoryBrowser } from "@/components/directory-browser";
import { Button } from "@/components/ui/button";
import { Play, RotateCcw, Info, CheckCircle2, AlertCircle, ChevronDown, ChevronUp, RefreshCw } from "lucide-react";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { toast } from "sonner";
import { Snapshot } from "@/types/snapshot";
import { MaintenanceOperation, MaintenanceOperationInfo, MAINTENANCE_OPERATIONS } from "@/types/maintenance";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { safeFormatDate } from "@/lib/date";
import { formatBytes } from "@/pages/tasks/tasks-page";

const COMMAND_TYPE_LABELS: Record<string, string> = {
  backup_now: "手动备份",
  restore_req: "恢复",
  selective_restore_req: "恢复",
  policy_push: "策略下发",
  snapshot_list_req: "快照刷新",
};

export function NodeDetailPage() {
  const { agentId } = useParams<{ agentId: string }>();
  const queryClient = useQueryClient();
  const [selectedSnapshot, setSelectedSnapshot] = useState<Snapshot | null>(null);
  const [targetPath, setTargetPath] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [expandedTaskId, setExpandedTaskId] = useState<string | null>(null);
  const [confirmBackupOpen, setConfirmBackupOpen] = useState(false);
  const [confirmMaintenance, setConfirmMaintenance] = useState<MaintenanceOperationInfo | null>(null);

  const { data: agent, isLoading: agentLoading } = useQuery({
    queryKey: ["agent", agentId],
    queryFn: () => getAgent(agentId!),
    enabled: !!agentId,
  });

  const { data: policies } = useQuery({
    queryKey: ["policies", { agent_id: agentId }],
    queryFn: () => listPolicies(agentId),
    enabled: !!agentId,
  });

  const { data: snapshots } = useQuery({
    queryKey: ["snapshots", agentId],
    queryFn: () => listSnapshots(agentId!),
    enabled: !!agentId,
  });

  const { data: tasks } = useQuery({
    queryKey: ["tasks", { agent_id: agentId }],
    queryFn: () => listTasks({ agent_id: agentId }),
    enabled: !!agentId,
  });

  const { data: commands, isFetching: commandsFetching, refetch: refetchCommands } = useQuery({
    queryKey: ["commands", agentId],
    queryFn: () => listAgentCommands(agentId!),
    enabled: !!agentId,
    refetchInterval: (query) => {
      const data = query.state.data;
      const hasActive = data?.some(
        (c) => c.status === "pending" || c.status === "dispatched" || c.status === "running"
      );
      return hasActive ? 5000 : false;
    },
  });

  const backupMutation = useMutation({
    mutationFn: () => backupNow(agentId!),
    onSuccess: (data) => {
      if (agent?.status === "online") {
        toast.success("备份命令已下发", { description: `Message ID: ${data.message_id}` });
      } else {
        toast.info("备份命令已排队", { description: "Agent 上线后将自动执行" });
      }
      queryClient.invalidateQueries({ queryKey: ["tasks"] });
      queryClient.invalidateQueries({ queryKey: ["commands"] });
    },
    onError: (error: any) => {
      toast.error("发起备份失败", { description: error.message });
    }
  });

  const restoreMutation = useMutation({
    mutationFn: (body: { snapshot_id: string; target_path: string }) =>
      restoreSnapshot(agentId!, body),
    onSuccess: (data) => {
      const msg = data.message === "restore queued" ? "恢复命令已排队" : "恢复任务已开始";
      toast.success(msg, { description: `Message ID: ${data.message_id}` });
      setSelectedSnapshot(null);
      setConfirmed(false);
      queryClient.invalidateQueries({ queryKey: ["tasks"] });
    },
    onError: (error: any) => {
      toast.error("发起恢复失败", { description: error.message });
    }
  });

  const maintenanceMutation = useMutation({
    mutationFn: (op: MaintenanceOperation) => triggerMaintenance(agentId!, op),
    onSuccess: (data) => {
      if (agent?.status === "online") {
        toast.success("维护命令已下发", { description: `Message ID: ${data.message_id}` });
      } else {
        toast.info("维护命令已排队", { description: "Agent 上线后将自动执行" });
      }
      queryClient.invalidateQueries({ queryKey: ["tasks"] });
      queryClient.invalidateQueries({ queryKey: ["commands"] });
    },
    onError: (error: any) => {
      toast.error("发起维护失败", { description: error.message });
    }
  });

  if (agentLoading) {
    return <div className="space-y-4"><Skeleton className="h-12 w-full" /><Skeleton className="h-[400px] w-full" /></div>;
  }

  if (!agent) {
    return <div>节点未找到</div>;
  }

  const handleRestoreClick = (s: Snapshot) => {
    setSelectedSnapshot(s);
    setTargetPath(s.paths[0] || "");
    setConfirmed(false);
  };

  const handleMaintenanceClick = (op: MaintenanceOperationInfo) => {
    if (op.danger) {
      setConfirmMaintenance(op);
    } else {
      maintenanceMutation.mutate(op.key);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="space-y-1">
          <h1 className="text-2xl font-bold tracking-tight">{agent.name}</h1>
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <StatusBadge status={agent.status} />
            <span>ID: {agent.id}</span>
          </div>
        </div>
        <div className="flex gap-2">
          <Button disabled={backupMutation.isPending} onClick={() => setConfirmBackupOpen(true)}>
            <Play className="mr-2 h-4 w-4" />
            立即备份
          </Button>
          <ConfirmDialog
            open={confirmBackupOpen}
            onOpenChange={setConfirmBackupOpen}
            title="确认立即备份"
            description={`将对节点 ${agent.name} 发起立即备份请求。`}
            onConfirm={() => {
              setConfirmBackupOpen(false);
              backupMutation.mutate();
            }}
            loading={backupMutation.isPending}
            variant="default"
            confirmText="立即备份"
          />
        </div>
      </div>

      <Tabs defaultValue="overview" className="w-full">
        <TabsList className="grid w-full grid-cols-6 lg:w-[720px]">
          <TabsTrigger value="overview">概览</TabsTrigger>
          <TabsTrigger value="policy">策略</TabsTrigger>
          <TabsTrigger value="snapshots">快照</TabsTrigger>
          <TabsTrigger value="tasks">任务</TabsTrigger>
          <TabsTrigger value="commands">命令</TabsTrigger>
          <TabsTrigger value="browser">文件浏览</TabsTrigger>
        </TabsList>
        
        <TabsContent value="overview" className="mt-6 space-y-4">
          <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
            <Card>
              <CardHeader>
                <CardTitle className="text-sm font-medium">系统信息</CardTitle>
              </CardHeader>
              <CardContent className="text-sm space-y-2">
                <div className="flex justify-between"><span className="text-muted-foreground">操作系统</span><span>{agent.os}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">架构</span><span>{agent.arch}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">主机名</span><span>{agent.hostname}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">Agent 版本</span><span>{agent.version ? `v${agent.version}` : "未知"}</span></div>
              </CardContent>
            </Card>
            <Card>
              <CardHeader>
                <CardTitle className="text-sm font-medium">连接状态</CardTitle>
              </CardHeader>
              <CardContent className="text-sm space-y-2">
                <div className="flex justify-between"><span className="text-muted-foreground">当前状态</span><StatusBadge status={agent.status} /></div>
                <div className="flex justify-between"><span className="text-muted-foreground">最后在线</span><span>{safeFormatDate(agent.last_seen, "yyyy-MM-dd HH:mm:ss", "从未")}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">创建时间</span><span>{safeFormatDate(agent.created_at, "yyyy-MM-dd HH:mm:ss")}</span></div>
              </CardContent>
            </Card>
          </div>
        </TabsContent>

        <TabsContent value="policy" className="mt-6">
          <Card>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>调度</TableHead>
                  <TableHead>备份路径</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {policies?.map((policy) => (
                  <TableRow key={policy.id}>
                    <TableCell className="font-mono text-xs">{policy.schedule}</TableCell>
                    <TableCell className="text-xs truncate max-w-[200px]">{policy.backup_dirs.join(", ")}</TableCell>
                    <TableCell><StatusBadge status={policy.synced ? "success" : "unsynced"} /></TableCell>
                    <TableCell className="text-right">
                      <Link to={`/policies?id=${policy.id}`} className="text-primary hover:underline text-sm">详情</Link>
                    </TableCell>
                  </TableRow>
                )) || <TableRow><TableCell colSpan={4} className="text-center py-4">无关联策略</TableCell></TableRow>}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>

        <TabsContent value="snapshots" className="mt-6">
           <Card>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>ID</TableHead>
                  <TableHead>时间</TableHead>
                  <TableHead>路径</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {snapshots?.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell className="font-mono text-xs">{s.id.substring(0, 8)}</TableCell>
                    <TableCell className="text-xs">{safeFormatDate(s.time, "yyyy-MM-dd HH:mm:ss")}</TableCell>
                    <TableCell className="text-xs truncate max-w-[300px]">{s.paths.join(", ")}</TableCell>
                    <TableCell className="text-right">
                      <Button variant="ghost" size="sm" className="text-primary" onClick={() => handleRestoreClick(s)}>
                        <RotateCcw className="mr-1 h-3 w-3" />
                        恢复
                      </Button>
                    </TableCell>
                  </TableRow>
                )) || <TableRow><TableCell colSpan={4} className="text-center py-4">暂无快照</TableCell></TableRow>}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>

        <TabsContent value="tasks" className="mt-6">
           <Card>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-10"></TableHead>
                  <TableHead>类型</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {tasks?.map((t) => (
                  <React.Fragment key={t.id}>
                    <TableRow className="group">
                      <TableCell>
                        <Button 
                          variant="ghost" 
                          size="icon" 
                          className="h-8 w-8"
                          onClick={() => setExpandedTaskId(expandedTaskId === t.id ? null : t.id)}
                        >
                          {expandedTaskId === t.id ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
                        </Button>
                      </TableCell>
                      <TableCell>{t.type === "backup" ? "备份" : "恢复"}</TableCell>
                      <TableCell><StatusBadge status={t.status} /></TableCell>
                      <TableCell className="text-xs text-muted-foreground">{safeFormatDate(t.created_at, "yyyy-MM-dd HH:mm:ss")}</TableCell>
                      <TableCell className="text-right">
                        <Button variant="ghost" size="sm" className="text-primary" onClick={() => setExpandedTaskId(expandedTaskId === t.id ? null : t.id)}>
                          详情
                        </Button>
                      </TableCell>
                    </TableRow>
                    {expandedTaskId === t.id && (
                      <TableRow className="bg-muted/30">
                        <TableCell colSpan={5}>
                          <div className="p-4 grid gap-4 md:grid-cols-2 text-xs">
                            <div className="space-y-2">
                              <div className="flex items-center gap-2">
                                <span className="font-semibold w-24">Message ID:</span>
                                <code className="bg-muted px-1 rounded">{t.message_id}</code>
                              </div>
                              {t.command_id && (
                                <div className="flex items-center gap-2">
                                  <span className="font-semibold w-24">Command ID:</span>
                                  <code className="bg-muted px-1 rounded">{t.command_id}</code>
                                </div>
                              )}
                              {t.snapshot_id && (
                                <div className="flex items-center gap-2">
                                  <span className="font-semibold w-24">Snapshot ID:</span>
                                  <code className="bg-muted px-1 rounded">{t.snapshot_id}</code>
                                </div>
                              )}
                              <div className="flex items-center gap-2">
                                <span className="font-semibold w-24">开始时间:</span>
                                <span>{safeFormatDate(t.started_at, "yyyy-MM-dd HH:mm:ss")}</span>
                              </div>
                              <div className="flex items-center gap-2">
                                <span className="font-semibold w-24">结束时间:</span>
                                <span>{safeFormatDate(t.finished_at, "yyyy-MM-dd HH:mm:ss")}</span>
                              </div>
                            </div>
                            <div className="space-y-2">
                              {t.duration_ms !== undefined && (
                                <div className="flex items-center gap-2">
                                  <span className="font-semibold w-24">耗时:</span>
                                  <span>{(t.duration_ms / 1000).toFixed(2)}s</span>
                                </div>
                              )}
                              {t.repo_size !== undefined && (
                                <div className="flex items-center gap-2">
                                  <span className="font-semibold w-24">仓库大小:</span>
                                  <span>{formatBytes(t.repo_size)}</span>
                                </div>
                              )}
                              {t.error_log && (
                                <div className="space-y-1">
                                  <span className="font-semibold block">错误日志:</span>
                                  <pre className="bg-red-50 text-red-600 p-2 rounded whitespace-pre-wrap font-mono text-[10px] max-h-32 overflow-y-auto">
                                    {t.error_log}
                                  </pre>
                                </div>
                              )}
                            </div>
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </React.Fragment>
                )) || <TableRow><TableCell colSpan={5} className="text-center py-4">暂无任务</TableCell></TableRow>}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>

        <TabsContent value="commands" className="mt-6">
          <Card>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
              <div className="space-y-1">
                <CardTitle className="text-base">命令队列</CardTitle>
                <CardDescription>下发给 Agent 的指令历史</CardDescription>
              </div>
              <Button 
                variant="outline" 
                size="icon" 
                onClick={() => refetchCommands()} 
                disabled={commandsFetching}
                className={commandsFetching ? "animate-spin" : ""}
              >
                <RefreshCw className="h-4 w-4" />
              </Button>
            </CardHeader>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>类型</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>尝试</TableHead>
                  <TableHead>创建时间</TableHead>
                  <TableHead>完成时间</TableHead>
                  <TableHead>错误信息</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {commands?.map((c) => (
                  <TableRow key={c.id}>
                    <TableCell className="font-medium">{COMMAND_TYPE_LABELS[c.type] || c.type}</TableCell>
                    <TableCell><StatusBadge status={c.status} /></TableCell>
                    <TableCell>{c.attempts}</TableCell>
                    <TableCell className="text-xs">{safeFormatDate(c.created_at, "MM-dd HH:mm:ss")}</TableCell>
                    <TableCell className="text-xs">{safeFormatDate(c.completed_at, "MM-dd HH:mm:ss")}</TableCell>
                    <TableCell className="text-xs text-red-500 max-w-[150px] truncate" title={c.error_message}>{c.error_message || "-"}</TableCell>
                  </TableRow>
                )) || <TableRow><TableCell colSpan={6} className="text-center py-4">暂无命令</TableCell></TableRow>}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>

        <TabsContent value="browser" className="mt-6">
          <Card>
            <CardHeader>
              <CardTitle>文件浏览器</CardTitle>
              <CardDescription>实时浏览 Agent 上的目录</CardDescription>
            </CardHeader>
            <CardContent>
              {agent.status === "online" ? (
                <DirectoryBrowser 
                  agentId={agent.id} 
                  onSelect={(path) => {
                    copyToClipboard(path);
                    toast.success(`路径已复制: ${path}`);
                  }} 
                />
              ) : (
                <div className="h-64 flex flex-col items-center justify-center text-muted-foreground border-2 border-dashed rounded-lg space-y-2">
                  <div className="h-2 w-2 rounded-full bg-red-500" />
                  <p>节点离线，无法使用文件浏览器</p>
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <Card>
        <CardHeader>
          <CardTitle>仓库维护</CardTitle>
          <CardDescription>对节点备份仓库执行维护操作。请在执行前阅读每项操作的说明。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {MAINTENANCE_OPERATIONS.map((op) => (
            <div
              key={op.key}
              className="flex items-start justify-between gap-4 rounded-lg border p-3"
            >
              <div className="space-y-1">
                <div className="flex items-center gap-2 text-sm font-medium">
                  {op.label}
                  {op.danger && (
                    <span className="rounded bg-red-100 px-1.5 py-0.5 text-[10px] font-semibold text-red-600">
                      危险操作
                    </span>
                  )}
                </div>
                <p className="text-xs text-muted-foreground">{op.description}</p>
              </div>
              <Button
                variant={op.danger ? "destructive" : "outline"}
                size="sm"
                disabled={maintenanceMutation.isPending}
                onClick={() => handleMaintenanceClick(op)}
              >
                执行
              </Button>
            </div>
          ))}
        </CardContent>
      </Card>

      <ConfirmDialog
        open={!!confirmMaintenance}
        onOpenChange={(open) => !open && setConfirmMaintenance(null)}
        title={`确认${confirmMaintenance?.label ?? ""}`}
        description={confirmMaintenance?.description ?? ""}
        variant="destructive"
        loading={maintenanceMutation.isPending}
        confirmText="确认执行"
        onConfirm={() => {
          if (confirmMaintenance) {
            maintenanceMutation.mutate(confirmMaintenance.key);
            setConfirmMaintenance(null);
          }
        }}
      />

      <Dialog open={!!selectedSnapshot} onOpenChange={(open) => !open && setSelectedSnapshot(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>恢复快照</DialogTitle>
            <DialogDescription>
              将快照 {selectedSnapshot?.id.substring(0, 8)} 恢复到指定路径。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-4">
            <div className="grid gap-2">
              <Label htmlFor="target-path">恢复目标路径</Label>
              <Input
                id="target-path"
                value={targetPath}
                onChange={(e) => setTargetPath(e.target.value)}
                placeholder="/path/to/restore"
              />
            </div>
            <div className="flex items-center space-x-2 bg-amber-50 p-3 rounded border border-amber-200">
              <Checkbox 
                id="confirm" 
                checked={confirmed} 
                onCheckedChange={(checked) => setConfirmed(checked as boolean)} 
              />
              <label
                htmlFor="confirm"
                className="text-xs font-medium leading-none peer-disabled:cursor-not-allowed peer-disabled:opacity-70 text-amber-800"
              >
                我理解恢复操作可能会覆盖目标路径下的现有文件
              </label>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setSelectedSnapshot(null)}>取消</Button>
            <Button 
              disabled={!confirmed || !targetPath || restoreMutation.isPending}
              onClick={() => restoreMutation.mutate({ 
                snapshot_id: selectedSnapshot!.id, 
                target_path: targetPath 
              })}
            >
              {restoreMutation.isPending ? "提交中..." : "确认恢复"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
