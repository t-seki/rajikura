import { Alert, Anchor, Stack, Text } from "@mantine/core";
import { secretLabels, type SettingsView } from "./settingsApi";

// どのページの上部にも出す帯。直るまで出し続ける（直ったかは GET /api/settings が決める）。
//   - 保存先が決まっていない: 録音だけが待たされている
//   - 復号できない秘密がある: 鍵のファイルが無い・合わない。入れ直すまで未設定として扱われる
//   - バックアップの取る先（backup_dir）が空: 自動のバックアップが止まっている
export function SettingsBanners({ view, onOpenSettings }: { view: SettingsView | null; onOpenSettings: () => void }) {
  if (!view) return null;
  const backupOff = view.settings.backup_dir === "";
  if (view.library_set && view.unreadable_secrets.length === 0 && !backupOff) return null;
  return (
    <Stack gap="xs" mb="md" data-testid="settings-banners">
      {!view.library_set && (
        <Alert color="orange" title="保存先を決めて" role="status">
          <Text size="sm">
            保存先が決まっていないので、録音は待たされている（番組表の同期と番組の操作は動く）。{" "}
            <Anchor component="button" type="button" size="sm" onClick={onOpenSettings}>
              設定を開く
            </Anchor>
          </Text>
        </Alert>
      )}
      {view.unreadable_secrets.length > 0 && (
        <Alert color="orange" title="秘密を入れ直して" role="status">
          <Text size="sm">
            次の秘密を復号できない（秘密の鍵のファイルが無いか、合っていない）ので、未設定として扱っている:{" "}
            {view.unreadable_secrets.map((n) => secretLabels[n] ?? n).join("、")}。{" "}
            <Anchor component="button" type="button" size="sm" onClick={onOpenSettings}>
              設定を開く
            </Anchor>
          </Text>
        </Alert>
      )}
      {backupOff && (
        <Alert color="orange" title="自動のバックアップが止まっている" role="status">
          <Text size="sm">
            バックアップの取る先が決まっていないので、自動ではバックアップを取っていない。DB を失うと戻せない。{" "}
            <Anchor component="button" type="button" size="sm" onClick={onOpenSettings}>
              設定を開く
            </Anchor>
          </Text>
        </Alert>
      )}
    </Stack>
  );
}
