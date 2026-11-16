import { Alert, AppShell, Badge, Burger, Button, Group, Image, NavLink, Text, Title, Anchor } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "wouter";
import { navigate, usePathname, useSearch } from "wouter/use-browser-location";
import { ShowsPage } from "./shows/ShowsPage";
import { KeywordsPage } from "./keywords/KeywordsPage";
import { JobsPage } from "./jobs/JobsPage";
import { GuidePage } from "./guide/GuidePage";
import { TokensPage } from "./tokens/TokensPage";
import { TrayLinkPage } from "./tray/TrayLinkPage";
import { NotificationsPage } from "./notifications/NotificationsPage";
import { SettingsPage } from "./settings/SettingsPage";
import { SettingsBanners } from "./settings/SettingsBanners";
import { fetchSettings, type SettingsView } from "./settings/settingsApi";
import { NotificationBanners } from "./notifications/NotificationBanners";
import { fetchNotificationStatus, markNotificationRead, type NotificationStatus } from "./notifications/notificationsApi";

// ナビゲーションのページと、その URL のパス（ADR 0024）。番組表は guide/、番組は shows/、キーワードは keywords/、ジョブは jobs/、通知は notifications/、設定は settings/、API トークンは tokens/ の画面。開く画面は下の routeOf が URL のパスで決める。ナビに入れない「連携」の画面（tray/、`/tray-link`）もある。
// pages の並びは docs/web-ui.md の「ナビは「…」の順で」と同じにする（internal/server/docs_test.go の TestNavOrderIsDocumented が確かめる）。
const pages = ["番組表", "番組", "キーワード", "ジョブ", "通知", "設定", "API トークン"] as const;

const pagePaths: Record<(typeof pages)[number], string> = {
  番組表: "/",
  番組: "/shows",
  キーワード: "/keywords",
  ジョブ: "/jobs",
  通知: "/notifications",
  設定: "/settings",
  "API トークン": "/tokens",
};

type Page = (typeof pages)[number] | "連携";

const decode = (s: string) => {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
};

// URL のパスで開く画面を決める。画面を切り替えると URL も変わる（ADR 0024）。`/shows/{名前}` は番組の編集、`/jobs/{id}` はジョブの詳細を開き、
// その名前・id を param に返す（通知の画面とトーストも、通知が指すジョブ・番組をこの URL で開く）。`/notifications`（指す先の無い通知のトーストと、
// トレイの起動の「未読 N 件」のトーストが開く）と `/tray-link`（トレイの連携が開く）は今のまま。知らないパスは known を false にする
// （番組表を開き、URL を `/` に置き換える）。pathname は URL に書かれたまま（エンコードしたまま）のものを受ける。
export function routeOf(pathname: string): { page: Page; param: string | null; known: boolean } {
  if (pathname === "/tray-link") return { page: "連携", param: null, known: true };
  const p = pages.find((x) => pagePaths[x] === pathname);
  if (p) return { page: p, param: null, known: true };
  const m = /^\/(shows|jobs)\/([^/]+)$/.exec(pathname);
  if (m) return { page: m[1] === "jobs" ? "ジョブ" : "番組", param: decode(m[2]), known: true };
  return { page: "番組表", param: null, known: false };
}

/**
 * 履歴の動かし方。push は履歴に積む（画面の切り替えと、詳細を開く）。replace は今の履歴を置き換える（放送日・検索の語）。
 * close は詳細を閉じる: 同じ画面の中で開いて積んだ詳細なら「戻る」と同じく 1 つ戻り、そうでなければ（URL で直接開いたなど）置き換える。
 */
export type HistoryMove = "push" | "replace" | "close";

// push で積んだ履歴の state に、積む前の URL を入れておく。close で、閉じた先の URL がそれと同じなら戻る（同じ履歴が 2 つ並ばないように）。
type HistoryState = { rajikuraBack?: string } | null;

function useGo() {
  // 戻るを頼んでから popstate が来るまでの間に、もう一度 close を頼まれても、2 つ戻らない
  const backPending = useRef(false);
  useEffect(() => {
    const done = () => {
      backPending.current = false;
    };
    window.addEventListener("popstate", done);
    return () => window.removeEventListener("popstate", done);
  }, []);
  return useCallback((to: string, how: HistoryMove = "push") => {
    const here = window.location.pathname + window.location.search;
    if (to === here) return;
    const state = window.history.state as HistoryState;
    if (how === "close") {
      if (backPending.current) return;
      if (state?.rajikuraBack === to) {
        backPending.current = true;
        window.history.back();
        return;
      }
      navigate(to, { replace: true });
    } else if (how === "replace") {
      navigate(to, { replace: true, state });
    } else {
      navigate(to, { state: { rajikuraBack: here } });
    }
  }, []);
}

// 未読の数と帯の元（通知の /api/notifications/status と、設定の /api/settings）を読み直す間隔。
const statusInterval = 60_000;

// version は /api/me の版。空ならナビに出さない。
export function Shell({ onLogout, version }: { onLogout: () => Promise<void>; version: string }) {
  const [opened, { toggle, close }] = useDisclosure();
  const pathname = usePathname();
  const search = useSearch();
  const go = useGo();
  const { page, param, known } = routeOf(pathname);
  useEffect(() => {
    if (!known) navigate("/", { replace: true });
  }, [known]);
  // 番組表の「番組を開く」「番組の設定を開く」と、キーワードの「足した放送回」の番組の名前は、その番組の編集の URL へ移る。
  const openShowPage = useCallback((name: string) => go(`/shows/${encodeURIComponent(name)}`), [go]);
  // 番組表の検索の「このキーワードで録音」で、キーワードの画面の作るダイアログに入れる語。開いたら null に戻す。
  // URL には載せない（再読み込みすると下書きは消える）。
  const [keywordDraft, setKeywordDraft] = useState<string | null>(null);
  const clearKeywordDraft = useCallback(() => setKeywordDraft(null), []);
  const openJob = useCallback((id: number) => go(`/jobs/${id}`), [go]);
  const closeJob = useCallback(() => go("/jobs", "close"), [go]);
  const closeEdit = useCallback(() => go("/shows", "close"), [go]);
  // 無いジョブ・無い番組の URL は、一覧の URL に置き換える（閉じるボタンの close と違い、一覧から開いていても戻らない）
  const missingJob = useCallback(() => navigate("/jobs", { replace: true }), []);
  const missingEdit = useCallback(() => navigate("/shows", { replace: true }), []);
  const guideNavigate = useCallback((s: string, how: HistoryMove) => go(`/${s}`, how), [go]);

  const [error, setError] = useState<string | null>(null);
  // 未読の数と帯の元。読めなければ前の値のまま（初めから読めなければ出さない）。
  const [status, setStatus] = useState<NotificationStatus | null>(null);

  // 設定の帯（保存先が無い・復号できない秘密）の元。読めなければ前の値のまま（初めから読めなければ出さない）。
  const [settings, setSettings] = useState<SettingsView | null>(null);

  const reloadBanners = useCallback(() => {
    fetchNotificationStatus().then(setStatus, () => {});
    fetchSettings().then(setSettings, () => {});
  }, []);

  // トレイのトーストが開く URL の `notification=<通知の id>` は、どの画面でも受ける: その 1 件を既読にし、結果を待たずに URL から
  // このキーだけを消す（ほかのクエリとパスは残し、履歴は積まない）。既読にできたら未読の数と帯を読み直す。失敗しても画面は
  // ふつうに出し、console.warn だけ出す。
  useEffect(() => {
    const params = new URLSearchParams(search);
    const raw = params.get("notification");
    if (raw === null) return;
    params.delete("notification");
    const rest = params.toString();
    navigate(pathname + (rest ? `?${rest}` : ""), { replace: true, state: window.history.state });
    const id = Number(raw);
    if (!/^\d+$/.test(raw) || !Number.isSafeInteger(id)) {
      console.warn("URL の notification が通知の id ではない:", raw);
      return;
    }
    markNotificationRead(id).then(reloadBanners, (e) => console.warn("通知を既読にできなかった:", e));
  }, [pathname, search, reloadBanners]);

  useEffect(() => {
    reloadBanners();
    const id = setInterval(reloadBanners, statusInterval);
    return () => clearInterval(id);
  }, [reloadBanners]);

  async function doLogout() {
    setError(null);
    try {
      await onLogout();
    } catch {
      setError("ログアウトできなかった。通信を確かめてほしい");
    }
  }

  return (
    <AppShell
      header={{ height: 56 }}
      navbar={{ width: 200, breakpoint: "sm", collapsed: { mobile: !opened } }}
      padding="md"
    >
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label="メニュー" />
            <Group gap="xs" wrap="nowrap">
              <Image src="/favicon.svg" alt="" w={28} h={28} />
              <Title order={3}>rajikura</Title>
            </Group>
          </Group>
          {/* ソースへのリンク（AGPL-3.0 第 13 条。ADR 0044）。マークはライトで黒・ダークで白の 1 色で、ホバーでも色を変えない */}
          <Anchor
            href="https://github.com/t-seki/rajikura"
            target="_blank"
            rel="noopener noreferrer"
            aria-label="ソース（GitHub のリポジトリ）"
            style={{ display: "flex", color: "light-dark(var(--mantine-color-black), var(--mantine-color-white))" }}
          >
            <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true" focusable="false">
              <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
            </svg>
          </Anchor>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar p="xs" component="nav" aria-label="ナビゲーション">
        {pages.map((p) => (
          <NavLink
            key={p}
            component={Link}
            href={pagePaths[p]}
            label={p}
            rightSection={
              p === "通知" && status && status.unread > 0 ? (
                <Badge size="sm" circle={status.unread < 10} aria-label={`未読の通知 ${status.unread} 件`}>
                  {status.unread > 99 ? "99+" : status.unread}
                </Badge>
              ) : undefined
            }
            active={p === page}
            onClick={(e: React.MouseEvent) => {
              // 今の URL と同じなら、同じ履歴を積まない
              if (pagePaths[p] === pathname + search) e.preventDefault();
              close();
            }}
          />
        ))}
        {/* ログアウトはナビの項目（NavLink）にしない。版の上に置く */}
        <Button variant="default" size="xs" mt="auto" mx="sm" onClick={() => {
            // 押したらメニューを閉じてから、失敗の Alert を見せる（狭い画面でメニューが Main を覆うかは未確認）
            close();
            doLogout();
          }}
        >
          ログアウト
        </Button>
        {version && (
          <Text size="xs" c="dimmed" mt="sm" px="sm" aria-label="rajikura の版">
            {version}
          </Text>
        )}
      </AppShell.Navbar>
      <AppShell.Main>
        {error && (
          <Alert color="red" role="alert" mb="md">
            {error}
          </Alert>
        )}
        <SettingsBanners view={settings} onOpenSettings={() => go("/settings")} />
        <NotificationBanners status={status} onOpenHistory={() => go("/notifications")} />
        {page === "連携" ? (
          <TrayLinkPage search={search} />
        ) : page === "番組表" ? (
          <GuidePage
            search={known ? search : ""}
            onNavigate={guideNavigate}
            onOpenShow={openShowPage}
            onCreateKeyword={(words) => {
              setKeywordDraft(words);
              go("/keywords");
            }}
          />
        ) : page === "番組" ? (
          <ShowsPage openName={param} onOpenEdit={openShowPage} onCloseEdit={closeEdit} onMissingEdit={missingEdit} />
        ) : page === "キーワード" ? (
          <KeywordsPage draftWords={keywordDraft} onDraftHandled={clearKeywordDraft} onOpenShow={openShowPage} />
        ) : page === "ジョブ" ? (
          <JobsPage openId={param} onOpenJob={openJob} onCloseJob={closeJob} onMissingJob={missingJob} />
        ) : page === "通知" ? (
          <NotificationsPage onRead={reloadBanners} onOpenLink={(link) => go(link)} />
        ) : page === "設定" ? (
          <SettingsPage onChanged={setSettings} onTestSent={reloadBanners} />
        ) : (
          <TokensPage />
        )}
      </AppShell.Main>
    </AppShell>
  );
}
