import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  listNotifications,
  createNotification,
  updateNotification,
  deleteNotification,
  testNotification,
} from "@/services/notifications";
import { NotificationConfig, NotificationInput } from "@/types/notification";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Plus, Bell, Settings2, Trash2, MoreHorizontal, Send, Check } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger, SheetFooter } from "@/components/ui/sheet";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Checkbox } from "@/components/ui/checkbox";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ErrorPanel } from "@/components/error-panel";
import { format } from "date-fns";
import { zhCN } from "date-fns/locale";
import { toast } from "sonner";

const EVENT_OPTIONS = [
  { id: "backup_failed", label: "备份失败" },
  { id: "agent_offline", label: "节点离线" },
];

type NotificationType = NotificationInput["type"];

const DEFAULT_SUBJECT_TEMPLATE = "[VaultFleet] {{.Title}} - {{.AgentName}}";
const DEFAULT_BODY_TEMPLATE = "{{.Title}}\nLevel: {{.Level}}\nAgent: {{.AgentName}}\nTime: {{.Timestamp}}\n\n{{.Body}}";

function defaultConfigForType(type: NotificationType): Record<string, unknown> {
  switch (type) {
    case "telegram":
      return { bot_token: "", chat_id: "" };
    case "webhook":
      return { url: "" };
    case "email":
      return {
        smtp_host: "",
        smtp_port: 587,
        smtp_security: "starttls",
        smtp_username: "",
        smtp_password: "",
        from: "",
        from_name: "VaultFleet",
        to: [],
        cc: [],
        bcc: [],
        subject_template: DEFAULT_SUBJECT_TEMPLATE,
        body_template: DEFAULT_BODY_TEMPLATE,
        body_format: "text",
      };
  }
}

function configString(config: Record<string, unknown>, key: string) {
  const value = config[key];
  if (typeof value === "string") return value;
  if (typeof value === "number") return String(value);
  return "";
}

function configListText(config: Record<string, unknown>, key: string) {
  const value = config[key];
  if (Array.isArray(value)) {
    return value.filter((item): item is string => typeof item === "string").join("\n");
  }
  return typeof value === "string" ? value : "";
}

function parseConfigList(value: string) {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function NotificationsPage() {
  const queryClient = useQueryClient();
  const [isDrawerOpen, setIsDrawerOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
  const [testSuccessId, setTestSuccessId] = useState<string | null>(null);

  const [formData, setFormData] = useState<NotificationInput>({
    name: "",
    type: "telegram",
    config: defaultConfigForType("telegram"),
    events: ["backup_failed", "agent_offline"],
  });

  const { data: notifications, isLoading } = useQuery({ queryKey: ["notifications"], queryFn: listNotifications });

  const createMutation = useMutation({
    mutationFn: createNotification,
    onSuccess: () => {
      setIsDrawerOpen(false);
      queryClient.invalidateQueries({ queryKey: ["notifications"] });
      toast.success("通知配置已创建");
    },
    onError: (error: any) => {
      toast.error("创建通知失败", { description: error.message });
    },
  });

  const updateMutation = useMutation({
    mutationFn: (data: NotificationInput) => updateNotification(editingId!, data),
    onSuccess: () => {
      setIsDrawerOpen(false);
      queryClient.invalidateQueries({ queryKey: ["notifications"] });
      toast.success("通知配置已更新");
    },
    onError: (error: any) => {
      toast.error("更新通知失败", { description: error.message });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: deleteNotification,
    onSuccess: () => {
      setConfirmDeleteId(null);
      queryClient.invalidateQueries({ queryKey: ["notifications"] });
      toast.success("通知配置已删除");
    },
    onError: (error: any) => {
      toast.error("删除通知失败", { description: error.message });
    },
  });

  const testMutation = useMutation({
    mutationFn: testNotification,
    onSuccess: (_, id) => {
      setTestSuccessId(id);
      toast.success("测试消息已发送");
      setTimeout(() => setTestSuccessId(null), 3000);
    },
    onError: (error: any) => {
      toast.error("发送测试消息失败", { description: error.message });
    },
  });

  const handleEdit = (n: NotificationConfig) => {
    setEditingId(n.id);
    setFormData({
      name: n.name,
      type: n.type,
      config: n.config,
      events: n.events,
    });
    setIsDrawerOpen(true);
  };

  const handleDrawerClose = (open: boolean) => {
    setIsDrawerOpen(open);
    if (!open) {
      setEditingId(null);
      setFormData({
        name: "",
        type: "telegram",
        config: defaultConfigForType("telegram"),
        events: ["backup_failed", "agent_offline"],
      });
      createMutation.reset();
      updateMutation.reset();
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (editingId) {
      updateMutation.mutate(formData);
    } else {
      createMutation.mutate(formData);
    }
  };

  const toggleEvent = (eventId: string) => {
    const newEvents = formData.events.includes(eventId)
      ? formData.events.filter(id => id !== eventId)
      : [...formData.events, eventId];
    setFormData({ ...formData, events: newEvents });
  };

  const updateConfig = (key: string, value: unknown) => {
    setFormData({ ...formData, config: { ...formData.config, [key]: value } });
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">通知设置</h1>
        <Sheet open={isDrawerOpen} onOpenChange={handleDrawerClose}>
          <SheetTrigger asChild>
            <Button>
              <Plus className="mr-2 h-4 w-4" /> 添加通知
            </Button>
          </SheetTrigger>
          <SheetContent className="sm:max-w-xl">
            <SheetHeader>
              <SheetTitle>{editingId ? "编辑通知" : "添加新通知"}</SheetTitle>
              <SheetDescription>
                配置系统告警的发送渠道和触发事件。
              </SheetDescription>
            </SheetHeader>
            <form onSubmit={handleSubmit} className="space-y-6 py-6 pb-20">
              <ErrorPanel error={(createMutation.error || updateMutation.error) as any} />
              
              <div className="space-y-2">
                <Label htmlFor="name">名称</Label>
                <Input
                  id="name"
                  placeholder="如: 运维 Telegram"
                  value={formData.name}
                  onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                  required
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="type">通知类型</Label>
                <Select
                  value={formData.type}
                  onValueChange={(val: NotificationType) => setFormData({
                    ...formData, 
                    type: val, 
                    config: defaultConfigForType(val)
                  })}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="telegram">Telegram Bot</SelectItem>
                    <SelectItem value="webhook">Generic Webhook</SelectItem>
                    <SelectItem value="email">Email SMTP</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-4 border rounded-md p-4 bg-muted/20">
                <Label className="text-xs font-bold uppercase tracking-wider text-muted-foreground">渠道配置</Label>
                {formData.type === "telegram" ? (
                  <>
                    <div className="space-y-2">
                      <Label htmlFor="bot_token">Bot Token</Label>
                      <Input
                        id="bot_token"
                        type="password"
                        value={configString(formData.config, "bot_token")}
                        onChange={(e) => updateConfig("bot_token", e.target.value)}
                        placeholder={configString(formData.config, "bot_token") === "[redacted]" ? "已加密 (输入以修改)" : "123456789:ABC..."}
                        required={configString(formData.config, "bot_token") !== "[redacted]"}
                      />
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="chat_id">Chat ID</Label>
                      <Input
                        id="chat_id"
                        value={configString(formData.config, "chat_id")}
                        onChange={(e) => updateConfig("chat_id", e.target.value)}
                        placeholder="-100..."
                        required
                      />
                    </div>
                  </>
                ) : formData.type === "webhook" ? (
                  <div className="space-y-2">
                    <Label htmlFor="webhook_url">Webhook URL</Label>
                    <Input
                      id="webhook_url"
                      value={configString(formData.config, "url")}
                      onChange={(e) => updateConfig("url", e.target.value)}
                      placeholder="https://hooks.slack.com/..."
                      required
                    />
                  </div>
                ) : (
                  <>
                    <div className="grid grid-cols-2 gap-3">
                      <div className="space-y-2">
                        <Label htmlFor="smtp_host">SMTP 主机</Label>
                        <Input
                          id="smtp_host"
                          value={configString(formData.config, "smtp_host")}
                          onChange={(e) => updateConfig("smtp_host", e.target.value)}
                          placeholder="smtp.example.com"
                          required
                        />
                      </div>
                      <div className="space-y-2">
                        <Label htmlFor="smtp_port">端口</Label>
                        <Input
                          id="smtp_port"
                          type="number"
                          min={1}
                          max={65535}
                          value={configString(formData.config, "smtp_port")}
                          onChange={(e) => updateConfig("smtp_port", Number(e.target.value))}
                          required
                        />
                      </div>
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="smtp_security">加密方式</Label>
                      <Select value={configString(formData.config, "smtp_security") || "starttls"} onValueChange={(value) => updateConfig("smtp_security", value)}>
                        <SelectTrigger id="smtp_security">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="starttls">STARTTLS</SelectItem>
                          <SelectItem value="tls">TLS</SelectItem>
                          <SelectItem value="none">无</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <div className="space-y-2">
                        <Label htmlFor="smtp_username">用户名</Label>
                        <Input
                          id="smtp_username"
                          value={configString(formData.config, "smtp_username")}
                          onChange={(e) => updateConfig("smtp_username", e.target.value)}
                          placeholder="ops@example.com"
                        />
                      </div>
                      <div className="space-y-2">
                        <Label htmlFor="smtp_password">密码</Label>
                        <Input
                          id="smtp_password"
                          type="password"
                          value={configString(formData.config, "smtp_password")}
                          onChange={(e) => updateConfig("smtp_password", e.target.value)}
                          placeholder={configString(formData.config, "smtp_password") === "[redacted]" ? "已加密 (输入以修改)" : "SMTP 密码"}
                        />
                      </div>
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <div className="space-y-2">
                        <Label htmlFor="from">发件人邮箱</Label>
                        <Input
                          id="from"
                          type="email"
                          value={configString(formData.config, "from")}
                          onChange={(e) => updateConfig("from", e.target.value)}
                          placeholder="ops@example.com"
                          required
                        />
                      </div>
                      <div className="space-y-2">
                        <Label htmlFor="from_name">发件人名称</Label>
                        <Input
                          id="from_name"
                          value={configString(formData.config, "from_name")}
                          onChange={(e) => updateConfig("from_name", e.target.value)}
                          placeholder="VaultFleet"
                        />
                      </div>
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="email_to">收件人</Label>
                      <Textarea
                        id="email_to"
                        value={configListText(formData.config, "to")}
                        onChange={(e) => updateConfig("to", parseConfigList(e.target.value))}
                        placeholder="admin@example.com"
                        required
                      />
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <div className="space-y-2">
                        <Label htmlFor="email_cc">抄送</Label>
                        <Textarea
                          id="email_cc"
                          value={configListText(formData.config, "cc")}
                          onChange={(e) => updateConfig("cc", parseConfigList(e.target.value))}
                          placeholder="cc@example.com"
                        />
                      </div>
                      <div className="space-y-2">
                        <Label htmlFor="email_bcc">密送</Label>
                        <Textarea
                          id="email_bcc"
                          value={configListText(formData.config, "bcc")}
                          onChange={(e) => updateConfig("bcc", parseConfigList(e.target.value))}
                          placeholder="bcc@example.com"
                        />
                      </div>
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="subject_template">主题模板</Label>
                      <Input
                        id="subject_template"
                        value={configString(formData.config, "subject_template")}
                        onChange={(e) => updateConfig("subject_template", e.target.value)}
                        required
                      />
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="body_format">正文格式</Label>
                      <Select value={configString(formData.config, "body_format") || "text"} onValueChange={(value) => updateConfig("body_format", value)}>
                        <SelectTrigger id="body_format">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="text">纯文本</SelectItem>
                          <SelectItem value="html">HTML</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="body_template">正文模板</Label>
                      <Textarea
                        id="body_template"
                        className="min-h-36 font-mono text-xs"
                        value={configString(formData.config, "body_template")}
                        onChange={(e) => updateConfig("body_template", e.target.value)}
                        required
                      />
                    </div>
                  </>
                )}
              </div>

              <div className="space-y-4">
                <Label>触发事件</Label>
                <div className="grid gap-4">
                  {EVENT_OPTIONS.map((opt) => (
                    <div key={opt.id} className="flex items-center space-x-2">
                      <Checkbox 
                        id={`event-${opt.id}`} 
                        checked={formData.events.includes(opt.id)}
                        onCheckedChange={() => toggleEvent(opt.id)}
                      />
                      <label htmlFor={`event-${opt.id}`} className="text-sm font-medium leading-none cursor-pointer">
                        {opt.label}
                      </label>
                    </div>
                  ))}
                </div>
              </div>

              <div className="fixed bottom-0 right-0 left-0 bg-background border-t p-4 lg:left-auto lg:w-[var(--radix-sheet-width)]">
                 <Button type="submit" className="w-full" disabled={createMutation.isPending || updateMutation.isPending}>
                  {createMutation.isPending || updateMutation.isPending ? "正在保存..." : "保存通知配置"}
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
              <TableHead>名称</TableHead>
              <TableHead>类型</TableHead>
              <TableHead>订阅事件</TableHead>
              <TableHead className="text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading ? (
              <TableRow><TableCell colSpan={4} className="h-24 text-center">正在加载...</TableCell></TableRow>
            ) : notifications?.length === 0 ? (
              <TableRow><TableCell colSpan={4} className="h-24 text-center text-muted-foreground">暂无通知配置</TableCell></TableRow>
            ) : (
              notifications?.map((n) => (
                <TableRow key={n.id}>
                  <TableCell className="font-medium">{n.name}</TableCell>
                  <TableCell className="capitalize">{n.type}</TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {n.events.map(ev => (
                        <span key={ev} className="text-[10px] bg-muted px-1.5 py-0.5 rounded text-muted-foreground border">
                          {EVENT_OPTIONS.find(o => o.id === ev)?.label || ev}
                        </span>
                      ))}
                    </div>
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-2">
                       <Button 
                        variant="ghost" 
                        size="icon" 
                        onClick={() => testMutation.mutate(n.id)}
                        disabled={testMutation.isPending && testMutation.variables === n.id}
                        title="发送测试消息"
                      >
                        {testSuccessId === n.id ? <Check className="h-4 w-4 text-green-500" /> : <Send className="h-4 w-4" />}
                      </Button>
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button variant="ghost" size="icon"><MoreHorizontal className="h-4 w-4" /></Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem onClick={() => handleEdit(n)}>
                            <Settings2 className="mr-2 h-4 w-4" /> 编辑
                          </DropdownMenuItem>
                          <DropdownMenuItem className="text-red-600" onClick={() => setConfirmDeleteId(n.id)}>
                            <Trash2 className="mr-2 h-4 w-4" /> 删除
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
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
        title="确认删除通知配置？"
        description="系统将停止向此渠道发送告警。此操作不可撤销。"
        onConfirm={() => confirmDeleteId && deleteMutation.mutate(confirmDeleteId)}
        loading={deleteMutation.isPending}
      />
    </div>
  );
}
