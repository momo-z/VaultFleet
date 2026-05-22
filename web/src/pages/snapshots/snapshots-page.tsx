import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useSearchParams, useNavigate } from "react-router-dom";
import { listSnapshots, refreshSnapshots, restoreSnapshot } from "@/services/snapshots";
import { listAgents } from "@/services/agents";
import { Snapshot } from "@/types/snapshot";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { RefreshCw, Camera, Undo2, AlertCircle, CheckCircle2, Info } from "lucide-react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetFooter } from "@/components/ui/sheet";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { format } from "date-fns";
import { zhCN } from "date-fns/locale";
import { cn } from "@/lib/utils";
import { ErrorPanel } from "@/components/error-panel";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { toast } from "sonner";
import { listPolicies } from "@/services/policies";
import { listStorage } from "@/services/storage";

export function SnapshotsPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [searchParams, setSearchParams] = useSearchParams();
  const agentId = searchParams.get("agent_id") || "";

  const [selectedSnapshot, setSelectedSnapshot] = useState<Snapshot | null>(null);
  const [targetPath, setTargetPath] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [restoreSuccessId, setRestoreSuccessId] = useState<string | null>(null);

  const { data: agents } = useQuery({ queryKey: ["agents"], queryFn: listAgents });
  const { data: policies } = useQuery({ queryKey: ["policies"], queryFn: () => listPolicies() });
  const { data: storageList } = useQuery({ queryKey: ["storage"], queryFn: listStorage });
  const { data: snapshots, isLoading, isFetching } = useQuery({
    queryKey: ["snapshots", agentId],
    queryFn: () => listSnapshots(agentId),
    enabled: !!agentId,
  });

  const refreshMutation = useMutation({
    mutationFn: () => refreshSnapshots(agentId),
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ["snapshots", agentId] });
      if (data.message === "snapshot refresh queued") {
        toast.info("快照刷新已排队", { description: "Agent 离线，上线后将自动执行" });
      } else {
        toast.success("快照列表刷新成功");
      }
    },
    onError: (error: any) => {
      toast.error("刷新快照失败", { description: error.message });
    }
  });

  const restoreMutation = useMutation({
    mutationFn: (data: { snapshot_id: string; target_path: string }) => restoreSnapshot(agentId, data),
    onSuccess: (data) => {
      setRestoreSuccessId(data.message_id);
      const msg = data.message === "restore queued" ? "恢复命令已排队" : "恢复任务已开始";
      toast.success(msg);
    },
    onError: (error: any) => {
      toast.error("发起恢复失败", { description: error.message });
    }
  });

  const handleAgentChange = (val: string) => {
    const newParams = new URLSearchParams(searchParams);
    if (val && val !== "all") {
      newParams.set("agent_id", val);
    } else {
      newParams.delete("agent_id");
    }
    setSearchParams(newParams);
  };

  const handleOpenRestore = (s: Snapshot) => {
    setSelectedSnapshot(s);
    setTargetPath(s.paths[0] || "");
    setConfirmed(false);
    setRestoreSuccessId(null);
    restoreMutation.reset();
  };

  const handleRestore = (e: React.FormEvent) => {
    e.preventDefault();
    if (!confirmed) return;
    restoreMutation.mutate({
      snapshot_id: selectedSnapshot!.id,
      target_path: targetPath,
    });
  };

  const currentAgent = agents?.find(a => a.id === agentId);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">快照浏览</h1>
        <div className="flex items-center gap-2">
          <Select value={agentId} onValueChange={handleAgentChange}>
            <SelectTrigger className="w-[200px]">
              <SelectValue placeholder="选择节点查看快照" />
            </SelectTrigger>
            <SelectContent>
              {agents?.map(a => <SelectItem key={a.id} value={a.id}>{a.name}</SelectItem>)}
            </SelectContent>
          </Select>
          <Button 
            variant="outline" 
            size="icon" 
            disabled={!agentId || isFetching || refreshMutation.isPending}
            onClick={() => refreshMutation.mutate()}
            title="请求 Agent 刷新快照列表"
          >
            <RefreshCw className={cn("h-4 w-4", (isFetching || refreshMutation.isPending) && "animate-spin")} />
          </Button>
        </div>
      </div>

      {!agentId ? (
        <div className="space-y-6">
          <div className="flex flex-col items-center justify-center h-48 border-2 border-dashed rounded-lg text-muted-foreground space-y-4">
            <Camera className="h-12 w-12 opacity-20" />
            <p>请选择一个节点以查看其备份快照</p>
          </div>

          {policies && policies.length > 0 && (
            <div className="space-y-3">
              <div className="flex items-center gap-2">
                <Info className="h-4 w-4 text-muted-foreground" />
                <h3 className="text-sm font-medium">跨节点恢复</h3>
              </div>
              <p className="text-xs text-muted-foreground">
                如需将数据恢复到新节点，请在新节点上创建策略时使用相同的<strong>存储</strong>和<strong>仓库子路径</strong>。策略同步后，原有快照将自动出现在新节点下。
              </p>
              <div className="rounded-md border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>节点</TableHead>
                      <TableHead>仓库路径</TableHead>
                      <TableHead>存储</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {policies.map((p) => (
                      <TableRow key={p.id}>
                        <TableCell className="text-sm">{agents?.find(a => a.id === p.agent_id)?.name || p.agent_id}</TableCell>
                        <TableCell className="font-mono text-xs">{p.repo_path}</TableCell>
                        <TableCell className="text-xs text-muted-foreground">{storageList?.find(s => s.id === p.storage_id)?.name || p.storage_id.substring(0, 8)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </div>
          )}
        </div>
      ) : (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                <TableHead>时间</TableHead>
                <TableHead>包含路径</TableHead>
                <TableHead>主机 / 用户</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading ? (
                <TableRow><TableCell colSpan={5} className="h-24 text-center">正在加载...</TableCell></TableRow>
              ) : snapshots?.length === 0 ? (
                <TableRow><TableCell colSpan={5} className="h-24 text-center text-muted-foreground">该节点暂无快照</TableCell></TableRow>
              ) : (
                snapshots?.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell className="font-mono text-xs">{s.id.substring(0, 8)}</TableCell>
                    <TableCell className="text-xs">
                      {format(new Date(s.time), "yyyy-MM-dd HH:mm:ss", { locale: zhCN })}
                    </TableCell>
                    <TableCell className="text-xs truncate max-w-[300px]">
                      {s.paths.join(", ")}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {s.hostname} / {s.username}
                    </TableCell>
                    <TableCell className="text-right">
                      <Button variant="ghost" size="sm" onClick={() => handleOpenRestore(s)}>
                        <Undo2 className="mr-2 h-4 w-4" /> 恢复
                      </Button>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      )}

      <Sheet open={!!selectedSnapshot} onOpenChange={(open) => !open && setSelectedSnapshot(null)}>
        <SheetContent className="sm:max-w-md">
          <SheetHeader>
            <SheetTitle>恢复数据</SheetTitle>
            <SheetDescription>
              将快照数据恢复到 Agent 所在机器的目标路径。
            </SheetDescription>
          </SheetHeader>
          
          {restoreSuccessId ? (
            <div className="py-8 space-y-6 text-center">
              <div className="flex justify-center">
                <CheckCircle2 className="h-16 w-16 text-green-500" />
              </div>
              <div className="space-y-2">
                <h3 className="text-lg font-bold">恢复任务已提交</h3>
                <p className="text-sm text-muted-foreground">
                  任务已由 Agent 接受并正在后台运行。
                </p>
              </div>
              <div className="bg-muted p-4 rounded text-xs font-mono break-all">
                Message ID: {restoreSuccessId}
              </div>
              <div className="flex flex-col gap-2">
                <Button onClick={() => navigate(`/tasks?agent_id=${agentId}`)}>查看任务进度</Button>
                <Button variant="outline" onClick={() => setSelectedSnapshot(null)}>关闭</Button>
              </div>
            </div>
          ) : (
            <form onSubmit={handleRestore} className="space-y-6 py-6 pb-20">
              <ErrorPanel error={restoreMutation.error as any} />
              
              <div className="space-y-1">
                <Label className="text-muted-foreground">快照 ID</Label>
                <div className="font-mono text-sm bg-muted p-2 rounded">{selectedSnapshot?.id}</div>
              </div>

              <div className="space-y-1">
                <Label className="text-muted-foreground">快照时间</Label>
                <div className="text-sm">{selectedSnapshot && format(new Date(selectedSnapshot.time), "yyyy-MM-dd HH:mm:ss", { locale: zhCN })}</div>
              </div>

              <div className="space-y-2">
                <Label htmlFor="target_path">目标路径</Label>
                <Input
                  id="target_path"
                  value={targetPath}
                  onChange={(e) => setTargetPath(e.target.value)}
                  required
                  placeholder="如: /tmp/restore-data"
                />
                <p className="text-xs text-muted-foreground">注意：恢复操作可能会覆盖目标路径下的现有文件。</p>
              </div>

              <div className="flex items-start space-x-2 bg-amber-50 border border-amber-200 p-3 rounded">
                <Checkbox 
                  id="confirm-restore" 
                  checked={confirmed} 
                  onCheckedChange={(val) => setConfirmed(!!val)}
                />
                <div className="grid gap-1.5 leading-none">
                  <label htmlFor="confirm-restore" className="text-xs font-medium text-amber-900 leading-tight">
                    确认恢复：我了解此操作将在 Agent 节点上执行，且目标路径必须是可写的。
                  </label>
                </div>
              </div>

              <div className="fixed bottom-0 right-0 left-0 bg-background border-t p-4 lg:left-auto lg:w-[var(--radix-sheet-width)]">
                 <Button type="submit" className="w-full" disabled={!confirmed || restoreMutation.isPending}>
                  {restoreMutation.isPending ? "正在提交..." : "开始恢复"}
                </Button>
              </div>
            </form>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
