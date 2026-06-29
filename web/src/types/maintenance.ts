export type MaintenanceOperation =
  | "unlock"
  | "check"
  | "repair_index"
  | "repair_snapshots"
  | "prune";

export interface MaintenanceOperationInfo {
  key: MaintenanceOperation;
  label: string;
  description: string;
  danger: boolean;
}

export const MAINTENANCE_OPERATIONS: MaintenanceOperationInfo[] = [
  {
    key: "unlock",
    label: "解锁仓库",
    description: "清除卡住的仓库锁。备份因\"仓库已被锁定\"失败时使用。不会修改备份数据。",
    danger: false,
  },
  {
    key: "check",
    label: "检查完整性",
    description: "检查仓库完整性，只读，不修改任何数据。用于诊断仓库是否损坏。",
    danger: false,
  },
  {
    key: "repair_index",
    label: "重建索引",
    description: "重建仓库索引，修复索引与数据不一致。不会删除备份数据。",
    danger: false,
  },
  {
    key: "repair_snapshots",
    label: "修复快照",
    description: "剔除已损坏、无法读取的快照。会删除损坏的快照，仅在 check 报告损坏时使用。",
    danger: true,
  },
  {
    key: "prune",
    label: "清理冗余",
    description: "清理不再被任何快照引用的冗余数据，释放存储空间。会重写仓库数据，请勿中途中断。",
    danger: true,
  },
];
