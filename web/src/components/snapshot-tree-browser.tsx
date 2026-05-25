import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  ChevronDown,
  ChevronRight,
  FileText,
  Folder,
  FolderOpen,
  FolderSearch,
  RefreshCw,
} from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { browseSnapshot } from "@/services/snapshots";
import { SnapshotFileEntry } from "@/types/snapshot";

export interface SnapshotTreeBrowserProps {
  agentId: string;
  snapshotId: string;
  isAgentOnline: boolean;
  selectedPaths: string[];
  onSelectedPathsChange: (paths: string[]) => void;
}

interface TreeNode {
  name: string;
  path: string;
  type: "file" | "dir";
  size: number;
  mtime: string;
  children: TreeNode[] | null;
  loading: boolean;
  error?: string;
}

export function SnapshotTreeBrowser({
  agentId,
  snapshotId,
  isAgentOnline,
  selectedPaths,
  onSelectedPathsChange,
}: SnapshotTreeBrowserProps) {
  const [expanded, setExpanded] = useState(false);
  const [rootNodes, setRootNodes] = useState<TreeNode[]>([]);
  const [rootLoading, setRootLoading] = useState(false);
  const [rootError, setRootError] = useState<string | null>(null);
  const inflightRef = useRef<Set<string>>(new Set());

  const selectedPathSet = useMemo(() => new Set(selectedPaths), [selectedPaths]);

  useEffect(() => {
    setExpanded(false);
    setRootNodes([]);
    setRootLoading(false);
    setRootError(null);
  }, [agentId, snapshotId]);

  const loadChildren = useCallback(
    async (path?: string): Promise<TreeNode[]> => {
      const resp = await browseSnapshot(agentId, {
        snapshot_id: snapshotId,
        ...(path ? { path } : {}),
      });
      if (resp.error) {
        throw new Error(resp.error);
      }
      const entries = resp.entries ?? [];
      return sortEntries(entries).map((e) => ({
        name: getPathName(e.path),
        path: e.path,
        type: e.type,
        size: e.size,
        mtime: e.mtime,
        children: e.type === "dir" ? null : [],
        loading: false,
      }));
    },
    [agentId, snapshotId],
  );

  const handleExpand = useCallback(async () => {
    if (!isAgentOnline) return;
    setExpanded(true);
    setRootLoading(true);
    setRootError(null);
    try {
      const nodes = await loadChildren();
      setRootNodes(nodes);
    } catch (err: any) {
      setRootError(err?.message || "请求超时或 Agent 异常");
    } finally {
      setRootLoading(false);
    }
  }, [isAgentOnline, loadChildren]);

  const handleRefresh = useCallback(async () => {
    if (!isAgentOnline) return;
    setRootLoading(true);
    setRootError(null);
    try {
      const nodes = await loadChildren();
      setRootNodes(nodes);
    } catch (err: any) {
      setRootError(err?.message || "请求超时或 Agent 异常");
    } finally {
      setRootLoading(false);
    }
  }, [isAgentOnline, loadChildren]);

  const updateNodeAtPath = useCallback(
    (
      nodeList: TreeNode[],
      targetPath: string,
      updater: (node: TreeNode) => TreeNode,
    ): TreeNode[] => {
      return nodeList.map((node) => {
        if (node.path === targetPath) {
          return updater(node);
        }
        if (node.children && targetPath.startsWith(node.path + "/")) {
          return {
            ...node,
            children: updateNodeAtPath(node.children, targetPath, updater),
          };
        }
        return node;
      });
    },
    [],
  );

  const [expandedNodes, setExpandedNodes] = useState<Set<string>>(new Set());

  const handleToggle = useCallback(
    (path: string) => {
      const node = findNode(rootNodes, path);
      if (!node || node.type !== "dir") return;

      const isExpanded = expandedNodes.has(path);
      if (isExpanded) {
        setExpandedNodes((prev) => {
          const next = new Set(prev);
          next.delete(path);
          return next;
        });
        return;
      }

      setExpandedNodes((prev) => {
        const next = new Set(prev);
        next.add(path);
        return next;
      });

      if (node.children !== null) return;

      if (inflightRef.current.has(path)) return;
      inflightRef.current.add(path);

      setRootNodes((prev) =>
        updateNodeAtPath(prev, path, (n) => ({ ...n, loading: true })),
      );

      loadChildren(path)
        .then((children) => {
          setRootNodes((prev) =>
            updateNodeAtPath(prev, path, (n) => ({
              ...n,
              children,
              loading: false,
              error: undefined,
            })),
          );
        })
        .catch((err: any) => {
          setRootNodes((prev) =>
            updateNodeAtPath(prev, path, (n) => ({
              ...n,
              children: [],
              loading: false,
              error: err?.message || "加载失败",
            })),
          );
        })
        .finally(() => {
          inflightRef.current.delete(path);
        });
    },
    [rootNodes, expandedNodes, loadChildren, updateNodeAtPath],
  );

  const handleCheck = useCallback(
    (node: TreeNode, checked: boolean) => {
      if (checked) {
        if (!selectedPaths.includes(node.path)) {
          onSelectedPathsChange([...selectedPaths, node.path]);
        }
      } else {
        onSelectedPathsChange(selectedPaths.filter((p) => p !== node.path));
      }
    },
    [onSelectedPathsChange, selectedPaths],
  );

  if (!expanded) {
    return (
      <div className="rounded-md border bg-card p-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="w-full"
          disabled={!isAgentOnline}
          onClick={handleExpand}
        >
          <FolderSearch className="h-4 w-4" />
          {isAgentOnline ? "浏览快照内容" : "需要节点在线才能浏览"}
        </Button>
      </div>
    );
  }

  return (
    <div className="overflow-hidden rounded-md border bg-card">
      <div className="flex items-center justify-between gap-2 border-b bg-muted/50 px-3 py-2">
        <span className="min-w-0 truncate text-xs font-medium">快照内容</span>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="h-7 w-7 shrink-0"
          disabled={!isAgentOnline || rootLoading}
          onClick={handleRefresh}
          aria-label="刷新快照内容"
        >
          <RefreshCw className={cn("h-3.5 w-3.5", rootLoading && "animate-spin")} />
        </Button>
      </div>

      <div className="max-h-[350px] overflow-y-auto">
        {rootLoading ? (
          <LoadingRows />
        ) : rootError ? (
          <div className="p-4">
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertTitle>无法读取快照内容</AlertTitle>
              <AlertDescription className="text-xs">
                {rootError}
              </AlertDescription>
            </Alert>
          </div>
        ) : rootNodes.length === 0 ? (
          <div className="p-8 text-center text-sm text-muted-foreground">快照为空</div>
        ) : (
          <div className="py-1">
            {rootNodes.map((node) => (
              <TreeNodeRow
                key={node.path}
                node={node}
                depth={0}
                expandedNodes={expandedNodes}
                onToggle={handleToggle}
                selectedPathSet={selectedPathSet}
                onCheck={handleCheck}
              />
            ))}
          </div>
        )}
      </div>

      {selectedPaths.length > 0 && (
        <div className="flex items-center justify-between gap-3 border-t bg-muted/30 px-3 py-2">
          <span className="min-w-0 truncate text-xs text-muted-foreground">
            已选中 {selectedPaths.length} 项
          </span>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 shrink-0 px-2 text-xs"
            onClick={() => onSelectedPathsChange([])}
          >
            清除选择
          </Button>
        </div>
      )}
    </div>
  );
}

function TreeNodeRow({
  node,
  depth,
  expandedNodes,
  onToggle,
  selectedPathSet,
  onCheck,
}: {
  node: TreeNode;
  depth: number;
  expandedNodes: Set<string>;
  onToggle: (path: string) => void;
  selectedPathSet: Set<string>;
  onCheck: (node: TreeNode, checked: boolean) => void;
}) {
  const isDir = node.type === "dir";
  const isExpanded = expandedNodes.has(node.path);
  const checked = selectedPathSet.has(node.path);

  return (
    <>
      <div
        className="group flex min-h-8 items-center gap-1 px-2 py-1 hover:bg-muted/50"
        data-snapshot-tree-row=""
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
      >
        {isDir ? (
          <button
            type="button"
            className="flex h-5 w-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
            onClick={() => onToggle(node.path)}
            aria-label={`${isExpanded ? "折叠" : "展开"} ${node.path}`}
          >
            {node.loading ? (
              <RefreshCw className="h-3.5 w-3.5 animate-spin" />
            ) : isExpanded ? (
              <ChevronDown className="h-3.5 w-3.5" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5" />
            )}
          </button>
        ) : (
          <span className="h-5 w-5 shrink-0" aria-hidden="true" />
        )}

        <button
          type="button"
          className={cn(
            "flex min-w-0 flex-1 items-center gap-1.5 text-left",
            isDir ? "cursor-pointer" : "cursor-default",
          )}
          onClick={() => {
            if (isDir) onToggle(node.path);
          }}
          disabled={!isDir}
        >
          {isDir ? (
            isExpanded ? (
              <FolderOpen className="h-4 w-4 shrink-0 fill-blue-500/20 text-blue-500" />
            ) : (
              <Folder className="h-4 w-4 shrink-0 fill-blue-500/20 text-blue-500" />
            )
          ) : (
            <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          )}
          <span className="min-w-0 truncate text-xs text-foreground">{node.name}</span>
        </button>

        <span className="w-16 shrink-0 truncate text-right text-[10px] text-muted-foreground">
          {formatSize(node.size)}
        </span>

        <Checkbox
          checked={checked}
          onCheckedChange={(value) => onCheck(node, value === true)}
          className="shrink-0"
          aria-label={`选择 ${node.path}`}
        />
      </div>

      {isDir && isExpanded && node.error && (
        <div
          className="px-2 py-1 text-[10px] text-destructive"
          style={{ paddingLeft: `${(depth + 1) * 16 + 28}px` }}
        >
          {node.error}
        </div>
      )}

      {isDir && isExpanded && node.children !== null && (
        <>
          {node.children.length === 0 && !node.loading && !node.error ? (
            <div
              className="px-2 py-1 text-[10px] text-muted-foreground"
              style={{ paddingLeft: `${(depth + 1) * 16 + 28}px` }}
            >
              空目录
            </div>
          ) : (
            node.children.map((child) => (
              <TreeNodeRow
                key={child.path}
                node={child}
                depth={depth + 1}
                expandedNodes={expandedNodes}
                onToggle={onToggle}
                selectedPathSet={selectedPathSet}
                onCheck={onCheck}
              />
            ))
          )}
        </>
      )}
    </>
  );
}

function LoadingRows() {
  return (
    <div className="space-y-2 p-2">
      {[1, 2, 3, 4, 5].map((i) => (
        <Skeleton key={i} className="h-7 w-full" />
      ))}
      <p className="pt-2 text-center text-xs text-muted-foreground">正在读取快照内容...</p>
    </div>
  );
}

function sortEntries(entries: SnapshotFileEntry[]): SnapshotFileEntry[] {
  return [...entries].sort((a, b) => {
    if (a.type !== b.type) return a.type === "dir" ? -1 : 1;
    return a.path.localeCompare(b.path);
  });
}

function getPathName(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] || path;
}

function findNode(nodes: TreeNode[], path: string): TreeNode | null {
  for (const node of nodes) {
    if (node.path === path) return node;
    if (node.children) {
      const found = findNode(node.children, path);
      if (found) return found;
    }
  }
  return null;
}

function formatSize(bytes: number): string {
  if (bytes === 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}
