import { getSettings, setSettings, useSettings } from "../../../lib/settings.ts";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { NOTIFY_ROWS, notifyCell, notifyCellPatch, type NotifyEffect } from "../../notifications/prefs.ts";

const COLUMNS: [NotifyEffect, MsgKey][] = [
  ["unread", "noti.col_unread"],
  ["os", "noti.col_os"],
  ["voice", "noti.col_voice"],
];

// NotificationTable — the notification settings table: one row per group of notification kinds,
// one column per effect (features/notifications/prefs.ts). A real table, so a screen reader
// announces the row and column of every switch; each switch is a native checkbox with role
// "switch", so Tab and Space work without any key handling here. A cell that cannot be turned
// off is shown disabled and on, rather than left blank, so the column still reads as "this
// happens".
export function NotificationTable() {
  const tr = useT();
  const s = useSettings();
  return (
    <table className="noti-table">
      <caption className="noti-table-caption">{tr("noti.table_caption")}</caption>
      <thead>
        <tr>
          <th scope="col">{tr("noti.col_kind")}</th>
          {COLUMNS.map(([effect, label]) => (
            <th key={effect} scope="col">{tr(label)}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {NOTIFY_ROWS.map((r) => (
          <tr key={r.row}>
            <th scope="row">
              <span className="noti-table-row">{tr(r.label)}</span>
              <span className="noti-table-hint">{tr(r.hint)}</span>
            </th>
            {COLUMNS.map(([effect, label]) => {
              const fixed = effect === "unread" && !!r.unreadFixed;
              const name = tr("noti.cell_aria", { row: tr(r.label), effect: tr(label) });
              return (
                <td key={effect}>
                  <input
                    type="checkbox"
                    role="switch"
                    className="noti-switch"
                    checked={notifyCell(s, r.row, effect)}
                    disabled={fixed}
                    title={fixed ? tr("noti.cell_fixed") : undefined}
                    aria-label={fixed ? `${name} — ${tr("noti.cell_fixed")}` : name}
                    // Read the settings at the moment of the click, not the render's snapshot:
                    // the patch rewrites whole maps, and a stale copy would undo a cell another
                    // tab or a keyboard command changed in between.
                    onChange={(e) => setSettings(notifyCellPatch(getSettings(), r.row, effect, e.target.checked))}
                  />
                </td>
              );
            })}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
