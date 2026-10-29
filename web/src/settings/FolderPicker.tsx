import { Alert, Button, Group, Loader, Modal, Stack, Text, TextInput } from "@mantine/core";
import { useEffect, useState } from "react";
import { FoldersApiError, listFolders, parentToTry, type FolderListing } from "./foldersApi";

interface Props {
  opened: boolean;
  /** 開いたときの場所に使う、欄の今の値。 */
  initial: string;
  onClose: () => void;
  /** 「選ぶ」を押したとき、今の場所のパスで呼ぶ（保存はしない）。 */
  onSelect: (path: string) => void;
}

// serve のマシンのフォルダを選ぶダイアログ。上のパスの欄に打って「移動」でも、一覧のフォルダを押してもたどれる。
// 開いたときは欄の値を開き、開けなければ（400）在る先祖まで親へさかのぼる。根まで開けなければ根の一覧を開く。
export function FolderPicker({ opened, initial, onClose, onSelect }: Props) {
  const [listing, setListing] = useState<FolderListing | null>(null);
  const [address, setAddress] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function go(path: string) {
    setLoading(true);
    try {
      const l = await listFolders(path);
      setListing(l);
      setAddress(l.path);
      setError(null);
    } catch (e) {
      setError(e instanceof FoldersApiError ? e.message : "フォルダを読めなかった。通信を確かめてほしい");
    } finally {
      setLoading(false);
    }
  }

  // 開いたとき: initial を開き、400 なら親へさかのぼる。最初の理由を一覧の上に残す。
  useEffect(() => {
    if (!opened) return;
    let cancelled = false;
    (async () => {
      setLoading(true);
      setListing(null);
      setError(null);
      let firstError: string | null = null;
      let cur: string | null = initial.trim();
      while (!cancelled) {
        try {
          const l = await listFolders(cur ?? "");
          if (cancelled) return;
          setListing(l);
          setAddress(l.path);
          setError(firstError);
          setLoading(false);
          return;
        } catch (e) {
          if (!(e instanceof FoldersApiError)) {
            if (!cancelled) {
              setError("フォルダを読めなかった。通信を確かめてほしい");
              setLoading(false);
            }
            return;
          }
          if (cur === null || cur === "") {
            // 根の一覧も読めなかった。
            if (!cancelled) {
              setError(firstError ?? e.message);
              setLoading(false);
            }
            return;
          }
          firstError ??= e.message;
          cur = parentToTry(cur) ?? "";
        }
      }
    })();
    return () => {
      cancelled = true;
    };
    // 開くたびに 1 回だけ。initial の変化では読み直さない。
  }, [opened]);

  return (
    <Modal opened={opened} onClose={onClose} title="フォルダを選ぶ" size="lg">
      <Stack>
        <Group align="flex-end" wrap="nowrap">
          <TextInput
            label="場所"
            value={address}
            onChange={(e) => setAddress(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void go(address.trim());
            }}
            style={{ flex: 1 }}
          />
          <Button variant="default" onClick={() => go(address.trim())} disabled={loading}>
            移動
          </Button>
        </Group>
        {error && (
          <Alert color="red" role="alert">
            {error}
          </Alert>
        )}
        <Group>
          <Button variant="default" onClick={() => go(listing?.parent ?? "")} disabled={loading || !listing || listing.path === ""}>
            上へ
          </Button>
          {loading && <Loader size="xs" />}
        </Group>
        {listing && (
          <Stack gap={2} aria-label="フォルダの一覧">
            {listing.folders.length === 0 && (
              <Text size="sm" c="dimmed">
                フォルダは無い
              </Text>
            )}
            {listing.folders.map((f) => (
              <Button key={f.path} variant="subtle" justify="flex-start" onClick={() => go(f.path)} disabled={loading}>
                {f.name}
              </Button>
            ))}
          </Stack>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            閉じる
          </Button>
          <Button
            onClick={() => {
              if (listing && listing.path !== "") {
                onSelect(listing.path);
                onClose();
              }
            }}
            disabled={!listing || listing.path === ""}
          >
            選ぶ
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
