import { notifications } from "@mantine/notifications";

// 操作の成功を知らせるトースト。失敗は消えると読み逃すので、ここでは出さず画面の Alert で出す。
export function notifySuccess(message: string) {
  notifications.show({ message, color: "green", autoClose: 4000 });
}
