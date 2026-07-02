import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { listPolicies, createPolicy, updatePolicy, deletePolicy } from "@/services/policies";
import { listAgents, backupNow } from "@/services/agents";
import { listStorage } from "@/services/storage";

import { BackupPolicy, PolicyInput, RetentionConfig } from "@/types/policy";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Plus, Settings2, Trash2, MoreHorizontal, Play, ChevronDown } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger, SheetFooter } from "@/components/ui/sheet";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Checkbox } from "@/components/ui/checkbox";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ErrorPanel } from "@/components/error-panel";
import { DirectoryBrowser } from "@/components/directory-browser";

import { format } from "date-fns";
import { zhCN } from "date-fns/locale";
import { toast } from "sonner";
import { describeCron } from "@/lib/cron";

const RETENTION_PRESETS: Record<string, { label: string; description: string; values: { keep_last: number; keep_daily: number; keep_weekly: number; keep_monthly: number } }> = {
  basic: {
    label: "基础",
    description: "约 3 个月深度，适合非关键数据",
    values: { keep_last: 7, keep_daily: 7, keep_weekly: 4, keep_monthly: 3 },
  },
  standard: {
    label: "标准",
    description: "约半年深度，适合大多数场景",
    values: { keep_last: 10, keep_daily: 7, keep_weekly: 4, keep_monthly: 6 },
  },
  archive: {
    label: "长期归档",
    description: "约 1 年深度，适合重要业务数据",
    values: { keep_last: 10, keep_daily: 14, keep_weekly: 8, keep_monthly: 12 },
  },
  custom: {
    label: "自定义",
    description: "手动设置各维度保留数量",
    values: { keep_last: 7, keep_daily: 7, keep_weekly: 4, keep_monthly: 6 },
  },
};

const RCLONE_ARG_FIELDS = [
  {
    key: "transfers",
    label: "并发传输数",
    description: "同时上传的文件数",
    defaultValue: "4",
    webdavValue: "2",
  },
  {
    key: "tpslimit",
    label: "每秒请求数",
    description: "限制每秒 HTTP 请求数，0 = 不限",
    defaultValue: "0",
    webdavValue: "4",
  },
  {
    key: "retries",
    label: "重试次数",
    description: "失败后重试次数",
    defaultValue: "3",
    webdavValue: "10",
  },
  {
    key: "retries-sleep",
    label: "重试间隔",
    description: "重试之间的等待时间（如 10s）",
    defaultValue: "0",
    webdavValue: "10s",
  },
  {
    key: "low-level-retries",
    label: "底层重试",
    description: "底层 IO 错误重试次数",
    defaultValue: "10",
    webdavValue: "20",
  },
  {
    key: "timeout",
    label: "请求超时",
    description: "单次 HTTP 请求超时（如 5m0s）",
    defaultValue: "5m0s",
    webdavValue: "10m0s",
  },
] as const;

export function defaultPolicyInput(): PolicyInput {
  return {
    agent_id: "",
    storage_id: "",
    repo_path: "",
    restic_password: "",
    backup_dirs: [],
    exclude_patterns: ["/tmp", "/proc", "/sys", "/dev"],
    schedule: "0 2 * * *",
    retention: {
      keep_last: 10,
      keep_daily: 7,
      keep_weekly: 4,
      keep_monthly: 6,
    },
    rclone_args: {},
    timeout_hours: 6,
  };
}

export function defaultRcloneArgs(storageType: string): Record<string, string> {
  if (storageType.toLowerCase() !== "webdav") {
    return {};
  }
  return Object.fromEntries(RCLONE_ARG_FIELDS.map((field) => [field.key, field.webdavValue]));
}

export function cleanRcloneArgs(args?: Record<string, string>): Record<string, string> | undefined {
  const cleaned = Object.fromEntries(
    Object.entries(args ?? {})
      .map(([key, value]) => [key, value.trim()] as const)
      .filter(([, value]) => value.length > 0),
  );
  return Object.keys(cleaned).length > 0 ? cleaned : undefined;
}

export function submitRcloneArgs(args: Record<string, string> | undefined, clearWhenEmpty: boolean): Record<string, string> | undefined {
  const cleaned = cleanRcloneArgs(args);
  if (cleaned || !clearWhenEmpty) {
    return cleaned;
  }
  return {};
}

function detectRetentionPreset(retention: RetentionConfig): string {
  for (const [key, preset] of Object.entries(RETENTION_PRESETS)) {
    if (key === "custom") continue;
    const v = preset.values;
    if ((retention.keep_last ?? 0) === v.keep_last && (retention.keep_daily ?? 0) === v.keep_daily && (retention.keep_weekly ?? 0) === v.keep_weekly && (retention.keep_monthly ?? 0) === v.keep_monthly) {
      return key;
    }
  }
  return "custom";
}

export function PoliciesPage() {
  const queryClient = useQueryClient();
  const [isDrawerOpen, setIsDrawerOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [confirmBackupAgentId, setConfirmBackupAgentId] = useState<string | null>(null);
  const [retentionPreset, setRetentionPreset] = useState("standard");
  const [advancedTransferOpen, setAdvancedTransferOpen] = useState(false);

  const [formData, setFormData] = useState<PolicyInput>(() => defaultPolicyInput());

  const { data: policies, isLoading } = useQuery({ queryKey: ["policies"], queryFn: () => listPolicies() });
  const { data: agents } = useQuery({ queryKey: ["agents"], queryFn: listAgents });
  const { data: storageList } = useQuery({ queryKey: ["storage"], queryFn: listStorage });

  const resetPolicyFormState = () => {
    setEditingId(null);
    setFormData(defaultPolicyInput());
    setRetentionPreset("standard");
    setAdvancedTransferOpen(false);
  };

  const createMutation = useMutation({
    mutationFn: createPolicy,
    onSuccess: () => {
      resetPolicyFormState();
      setIsDrawerOpen(false);
      queryClient.invalidateQueries({ queryKey: ["policies"] });
      toast.success("策略已创建");
    },
    onError: (error: any) => {
      toast.error("创建策略失败", { description: error.message });
    },
  });

  const updateMutation = useMutation({
    mutationFn: (data: PolicyInput) => updatePolicy(editingId!, data),
    onSuccess: () => {
      resetPolicyFormState();
      setIsDrawerOpen(false);
      queryClient.invalidateQueries({ queryKey: ["policies"] });
      toast.success("策略已更新");
    },
    onError: (error: any) => {
      toast.error("更新策略失败", { description: error.message });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: deletePolicy,
    onSuccess: () => {
      setConfirmDeleteId(null);
      queryClient.invalidateQueries({ queryKey: ["policies"] });
      toast.success("策略已删除");
    },
    onError: (error: any) => {
      toast.error("删除策略失败", { description: error.message });
    },
  });

  const backupMutation = useMutation({
    mutationFn: (agentId: string) => backupNow(agentId),
    onSuccess: (data) => {
      setConfirmBackupAgentId(null);
      const agent = agents?.find(a => a.id === confirmBackupAgentId);
      if (agent?.status === "online") {
        toast.success("备份命令已下发", { description: `Message ID: ${data.message_id}` });
      } else {
        toast.info("备份命令已排队", { description: "节点上线后将自动执行" });
      }
      queryClient.invalidateQueries({ queryKey: ["tasks"] });
    },
    onError: (error: any) => {
      toast.error("发起备份失败", { description: error.message });
    },
  });

  const handleEdit = (policy: BackupPolicy) => {
    setEditingId(policy.id);
    const repoSuffix = policy.repo_path.startsWith("vaultfleet/")
      ? policy.repo_path.slice("vaultfleet/".length)
      : policy.repo_path;
    setFormData({
      agent_id: policy.agent_id,
      storage_id: policy.storage_id,
      repo_path: repoSuffix,
      backup_dirs: policy.backup_dirs,
      exclude_patterns: policy.exclude_patterns,
      schedule: policy.schedule,
      retention: policy.retention,
      rclone_args: policy.rclone_args || {},
      timeout_hours: policy.timeout_hours || 6,
    });
    setRetentionPreset(detectRetentionPreset(policy.retention));
    setAdvancedTransferOpen(!!cleanRcloneArgs(policy.rclone_args));
    setIsDrawerOpen(true);
  };

  const handleDrawerClose = (open: boolean) => {
    setIsDrawerOpen(open);
    if (!open) {
      resetPolicyFormState();
      createMutation.reset();
      updateMutation.reset();
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const submitData = {
      ...formData,
      repo_path: "vaultfleet/" + formData.repo_path,
      rclone_args: submitRcloneArgs(formData.rclone_args, !!editingId),
      timeout_hours: formData.timeout_hours || 6,
    };
    if (editingId) {
      updateMutation.mutate(submitData);
    } else {
      createMutation.mutate(submitData);
    }
  };

  const selectedAgent = agents?.find(a => a.id === formData.agent_id);
  const isAgentOnline = selectedAgent?.status === "online";

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">备份策略</h1>
        <Sheet open={isDrawerOpen} onOpenChange={handleDrawerClose}>
          <SheetTrigger asChild>
            <Button>
              <Plus className="mr-2 h-4 w-4" /> 添加策略
            </Button>
          </SheetTrigger>
          <SheetContent className="sm:max-w-xl overflow-y-auto">
            <SheetHeader>
              <SheetTitle>{editingId ? "编辑策略" : "添加新策略"}</SheetTitle>
              <SheetDescription>
                定义哪些数据需要备份、备份到哪里以及备份的频率。
              </SheetDescription>
            </SheetHeader>

              <form aria-label="备份策略表单" onSubmit={handleSubmit} className="space-y-6 py-6 pb-20">
                <ErrorPanel error={(createMutation.error || updateMutation.error) as any} />
                
                <div className="grid grid-cols-2 gap-4">
                  <div className="space-y-2">
                    <Label>选择节点</Label>
                    <Select
                      value={formData.agent_id}
                      onValueChange={(val) => {
                        const agent = agents?.find(a => a.id === val);
                        const updates: Partial<PolicyInput> = { agent_id: val };
                        if (!editingId && agent) {
                          updates.repo_path = agent.name;
                        }
                        setFormData({ ...formData, ...updates });
                      }}
                      disabled={!!editingId}
                    >
                      <SelectTrigger><SelectValue placeholder="请选择节点" /></SelectTrigger>
                      <SelectContent>
                        {agents?.map(a => <SelectItem key={a.id} value={a.id}>{a.name}</SelectItem>)}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="space-y-2">
                    <Label>选择存储</Label>
                    <Select 
                      value={formData.storage_id} 
                      onValueChange={(val) => {
                        const storage = storageList?.find(s => s.id === val);
                        const updates: Partial<PolicyInput> = { storage_id: val };
                        if (!editingId && !cleanRcloneArgs(formData.rclone_args) && storage) {
                          const defaults = defaultRcloneArgs(storage.rclone_type);
                          updates.rclone_args = defaults;
                          if (Object.keys(defaults).length > 0) {
                            setAdvancedTransferOpen(true);
                          }
                        }
                        setFormData({ ...formData, ...updates });
                      }}
                      disabled={!!editingId}
                    >
                      <SelectTrigger><SelectValue placeholder="请选择存储" /></SelectTrigger>
                      <SelectContent>
                        {storageList?.map(s => <SelectItem key={s.id} value={s.id}>{s.name}</SelectItem>)}
                      </SelectContent>
                    </Select>
                  </div>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="repo_path">仓库子路径</Label>
                  <div className="flex">
                    <span className="inline-flex items-center rounded-l-md border border-r-0 border-input bg-muted px-3 text-sm text-muted-foreground">
                      vaultfleet/
                    </span>
                    <Input
                      id="repo_path"
                      className="rounded-l-none"
                      value={formData.repo_path}
                      onChange={(e) => setFormData({ ...formData, repo_path: e.target.value })}
                      placeholder={selectedAgent?.name || "my-server"}
                      disabled={!!editingId}
                    />
                  </div>
                  <p className="text-xs text-muted-foreground">备份仓库的唯一标识。更换节点后使用相同路径即可访问原有备份数据。</p>
                </div>

                {!editingId && (
                   <div className="space-y-2">
                    <Label htmlFor="restic_password">Restic 密码 (可选)</Label>
                    <Input
                      id="restic_password"
                      type="password"
                      value={formData.restic_password}
                      onChange={(e) => setFormData({ ...formData, restic_password: e.target.value })}
                      placeholder="留空则不加密，仍使用 restic 保留历史版本"
                    />
                  </div>
                )}

                <div className="space-y-4">
                  <Label htmlFor="backup_dirs">备份目录</Label>
                  <Textarea
                    id="backup_dirs"
                    value={formData.backup_dirs.join("\n")}
                    onChange={(e) => setFormData({ ...formData, backup_dirs: e.target.value.split("\n").filter(Boolean) })}
                    placeholder="每行一个路径，如: /etc"
                    rows={3}
                  />
                  {formData.agent_id && (
                    <div className="space-y-2">
                      <Label className="text-xs font-normal text-muted-foreground">通过文件浏览器添加：</Label>
                      {isAgentOnline ? (
                        <DirectoryBrowser
                          agentId={formData.agent_id}
                          selectedPaths={formData.backup_dirs}
                          onSelect={(path) => {
                            if (!formData.backup_dirs.includes(path)) {
                              setFormData({ ...formData, backup_dirs: [...formData.backup_dirs, path] });
                            }
                          }}
                          onDeselect={(path) => {
                            setFormData({ ...formData, backup_dirs: formData.backup_dirs.filter(d => d !== path) });
                          }}
                        />
                      ) : (
                        <div className="text-xs p-4 border border-dashed rounded text-center text-muted-foreground">
                          节点离线，无法使用文件浏览器。请手动输入路径。
                        </div>
                      )}
                    </div>
                  )}
                </div>

                <div className="space-y-2">
                  <Label htmlFor="schedule">Cron 调度</Label>
                  <Input
                    id="schedule"
                    value={formData.schedule}
                    onChange={(e) => setFormData({ ...formData, schedule: e.target.value })}
                    placeholder="0 2 * * *"
                  />
                  <p className="text-xs text-muted-foreground">
                    {describeCron(formData.schedule)}
                    {" — "}标准 Cron 表达式（分 时 日 月 周）。
                  </p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="timeout_hours">任务超时（小时）</Label>
                  <Input
                    id="timeout_hours"
                    type="number"
                    min={1}
                    max={72}
                    value={formData.timeout_hours ?? ""}
                    onChange={(e) => {
                      const value = e.target.value;
                      setFormData({ ...formData, timeout_hours: value === "" ? undefined : parseInt(value, 10) || 6 });
                    }}
                    className="h-9"
                  />
                  <p className="text-xs text-muted-foreground">备份任务超过此时间未完成将自动标记为超时，默认 6 小时</p>
                </div>

                <div className="space-y-4 border-t pt-4">
                  <div className="space-y-1">
                    <Label>保留策略 (Retention)</Label>
                    <p className="text-xs text-muted-foreground">每次备份后自动清理旧快照，释放存储空间。</p>
                  </div>
                  <div className="grid grid-cols-2 gap-2">
                    {Object.entries(RETENTION_PRESETS).map(([key, preset]) => (
                      <button
                        key={key}
                        type="button"
                        onClick={() => {
                          setRetentionPreset(key);
                          if (key !== "custom") {
                            setFormData({ ...formData, retention: { ...preset.values } });
                          }
                        }}
                        className={`rounded-lg border p-3 text-left transition-colors ${
                          retentionPreset === key
                            ? "border-primary bg-primary/5 ring-1 ring-primary"
                            : "border-border hover:border-muted-foreground/30"
                        }`}
                      >
                        <div className="text-sm font-medium">{preset.label}</div>
                        <div className="text-[11px] text-muted-foreground mt-0.5">{preset.description}</div>
                      </button>
                    ))}
                  </div>
                  {retentionPreset === "custom" && (
                    <div className="grid grid-cols-2 gap-4 pt-2">
                      <div className="space-y-1.5">
                        <Label className="text-xs">保留最近副本</Label>
                        <Input type="number" min={0} value={formData.retention.keep_last ?? 0} onChange={(e) => setFormData({ ...formData, retention: { ...formData.retention, keep_last: parseInt(e.target.value) || 0 }})} />
                        <p className="text-[11px] text-muted-foreground">始终保留最近 N 个快照</p>
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">保留每日副本</Label>
                        <Input type="number" min={0} value={formData.retention.keep_daily ?? 0} onChange={(e) => setFormData({ ...formData, retention: { ...formData.retention, keep_daily: parseInt(e.target.value) || 0 }})} />
                        <p className="text-[11px] text-muted-foreground">每天保留 1 个，共 N 天</p>
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">保留每周副本</Label>
                        <Input type="number" min={0} value={formData.retention.keep_weekly ?? 0} onChange={(e) => setFormData({ ...formData, retention: { ...formData.retention, keep_weekly: parseInt(e.target.value) || 0 }})} />
                        <p className="text-[11px] text-muted-foreground">每周保留 1 个，共 N 周</p>
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">保留每月副本</Label>
                        <Input type="number" min={0} value={formData.retention.keep_monthly ?? 0} onChange={(e) => setFormData({ ...formData, retention: { ...formData.retention, keep_monthly: parseInt(e.target.value) || 0 }})} />
                        <p className="text-[11px] text-muted-foreground">每月保留 1 个，共 N 个月</p>
                      </div>
                    </div>
                  )}
                  {retentionPreset !== "custom" && (
                    <div className="text-xs text-muted-foreground bg-muted/50 rounded-md px-3 py-2">
                      最近 {formData.retention.keep_last ?? 0} 个 · 每日 {formData.retention.keep_daily ?? 0} 份 · 每周 {formData.retention.keep_weekly ?? 0} 份 · 每月 {formData.retention.keep_monthly ?? 0} 份
                    </div>
                  )}
                </div>

                <div className="space-y-3 border-t pt-4">
                  <button
                    type="button"
                    className="flex w-full items-center justify-between text-left"
                    onClick={() => setAdvancedTransferOpen((open) => !open)}
                    aria-expanded={advancedTransferOpen}
                    aria-controls="advanced-transfer-params"
                  >
                    <span className="flex items-center gap-2">
                      <Settings2 className="h-4 w-4 text-muted-foreground" />
                      <span className="text-sm font-medium">高级传输参数</span>
                    </span>
                    <ChevronDown
                      className={`h-4 w-4 text-muted-foreground transition-transform ${advancedTransferOpen ? "rotate-180" : ""}`}
                    />
                  </button>

                  {advancedTransferOpen && (
                    <div id="advanced-transfer-params" className="grid grid-cols-1 gap-4 pt-1 sm:grid-cols-2">
                      {RCLONE_ARG_FIELDS.map((field) => (
                        <div key={field.key} className="space-y-1.5">
                          <Label htmlFor={`rclone-${field.key}`} className="text-xs">{field.label}</Label>
                          <Input
                            id={`rclone-${field.key}`}
                            value={formData.rclone_args?.[field.key] ?? ""}
                            onChange={(e) => {
                              setFormData({
                                ...formData,
                                rclone_args: {
                                  ...(formData.rclone_args ?? {}),
                                  [field.key]: e.target.value,
                                },
                              });
                            }}
                            placeholder={`默认 ${field.defaultValue} / WebDAV ${field.webdavValue}`}
                          />
                          <p className="text-[11px] text-muted-foreground">{field.description}</p>
                        </div>
                      ))}
                    </div>
                  )}
                </div>

                <div className="fixed bottom-0 right-0 left-0 bg-background border-t p-4 lg:left-auto lg:w-[var(--radix-sheet-width)]">
                   <Button type="submit" className="w-full" disabled={createMutation.isPending || updateMutation.isPending}>
                    {createMutation.isPending || updateMutation.isPending ? "正在提交..." : "提交策略"}
                  </Button>
                </div>
              </form>
          </SheetContent>
        </Sheet>
      </div>

      <div className="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>节点</TableHead>
              <TableHead>调度</TableHead>
              <TableHead>同步状态</TableHead>
              <TableHead className="hidden md:table-cell">创建时间</TableHead>
              <TableHead className="text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading ? (
              <TableRow><TableCell colSpan={5} className="h-24 text-center">正在加载...</TableCell></TableRow>
            ) : policies?.length === 0 ? (
              <TableRow><TableCell colSpan={5} className="h-24 text-center text-muted-foreground">暂无备份策略</TableCell></TableRow>
            ) : (
              policies?.map((p) => (
                <TableRow key={p.id}>
                  <TableCell className="font-medium">
                    {agents?.find(a => a.id === p.agent_id)?.name || p.agent_id}
                  </TableCell>
                  <TableCell>
                    <div className="space-y-0.5">
                      <div className="font-mono text-xs">{p.schedule}</div>
                      <div className="text-[10px] text-muted-foreground">{describeCron(p.schedule)}</div>
                    </div>
                  </TableCell>
                  <TableCell>
                    <StatusBadge status={p.synced ? "success" : "unsynced"} />
                  </TableCell>
                  <TableCell className="hidden md:table-cell text-xs text-muted-foreground">
                    {format(new Date(p.created_at), "yyyy-MM-dd HH:mm", { locale: zhCN })}
                  </TableCell>
                  <TableCell className="text-right">
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button variant="ghost" size="icon"><MoreHorizontal className="h-4 w-4" /></Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => handleEdit(p)}>
                          <Settings2 className="mr-2 h-4 w-4" /> 编辑
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => setConfirmBackupAgentId(p.agent_id)}>
                          <Play className="mr-2 h-4 w-4" /> 立即备份
                        </DropdownMenuItem>
                        <DropdownMenuItem className="text-red-600" onClick={() => setConfirmDeleteId(p.id)}>
                          <Trash2 className="mr-2 h-4 w-4" /> 删除
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>

      <ConfirmDialog
        open={!!confirmDeleteId}
        onOpenChange={(open) => !open && setConfirmDeleteId(null)}
        title="确认删除备份策略？"
        description="此操作将停止该节点的自动备份任务。存储中的备份数据不会被删除。"
        onConfirm={() => confirmDeleteId && deleteMutation.mutate(confirmDeleteId)}
        loading={deleteMutation.isPending}
      />

      <ConfirmDialog
        open={!!confirmBackupAgentId}
        onOpenChange={(open) => !open && setConfirmBackupAgentId(null)}
        title="确认立即备份"
        description={`将对节点 ${agents?.find(a => a.id === confirmBackupAgentId)?.name ?? confirmBackupAgentId} 发起立即备份请求。`}
        onConfirm={() => confirmBackupAgentId && backupMutation.mutate(confirmBackupAgentId)}
        loading={backupMutation.isPending}
        confirmText="立即备份"
      />
    </div>
  );
}
