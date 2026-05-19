import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { changePassword, exportSystemData } from "@/services/system";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle, CardFooter } from "@/components/ui/card";
import { ErrorPanel } from "@/components/error-panel";
import { Download, ShieldCheck, CheckCircle2 } from "lucide-react";

export function SystemPage() {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordSuccess, setPasswordSuccess] = useState(false);

  const passwordMutation = useMutation({
    mutationFn: changePassword,
    onSuccess: () => {
      setPasswordSuccess(true);
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      setTimeout(() => setPasswordSuccess(false), 3000);
    },
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
    },
  });

  const handlePasswordSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (newPassword !== confirmPassword) {
      alert("新密码不匹配");
      return;
    }
    passwordMutation.mutate({ current_password: currentPassword, new_password: newPassword });
  };

  return (
    <div className="space-y-6 max-w-4xl">
      <h1 className="text-2xl font-bold tracking-tight">系统管理</h1>

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
            <CardTitle>数据导出</CardTitle>
            <CardDescription>导出 Master 节点的完整数据库。建议在进行系统迁移或重大更新前导出备份。</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex flex-col items-center justify-center py-8 text-center space-y-4 bg-muted/20 rounded-lg border-2 border-dashed">
              <ShieldCheck className="h-12 w-12 text-muted-foreground opacity-30" />
              <p className="text-sm text-muted-foreground px-6">
                导出的压缩包包含 SQLite 数据库文件。请务必加密存储导出的文件。
              </p>
            </div>
          </CardContent>
          <CardFooter>
            <Button 
              variant="outline" 
              className="w-full" 
              onClick={() => exportMutation.mutate()}
              disabled={exportMutation.isPending}
            >
              <Download className="mr-2 h-4 w-4" /> 
              {exportMutation.isPending ? "正在生成导出文件..." : "导出 Master 数据"}
            </Button>
          </CardFooter>
        </Card>
      </div>
    </div>
  );
}
