/**
 * Browser system notification for an incoming call.
 *
 * The browser shows it with the operating system's notification sound. It
 * needs no user interaction with the page, so it signals a call when the
 * browser blocks the ringtone or the tab is in the background. It shows only
 * when the user already granted notification permission; it never asks for
 * permission. The notification is local and contacts no external system.
 */

/** One tag replaces an older incoming-call notification instead of stacking. */
const INCOMING_CALL_TAG = 'chatto-incoming-call';

/**
 * Show the notification when permission is granted. Clicking it focuses the
 * page, where the incoming-call banner shows. Returns a function that closes it.
 */
export function showIncomingCallNotification(content: { title: string; body: string }): () => void {
  if (typeof Notification === 'undefined' || Notification.permission !== 'granted') {
    return () => {};
  }
  let notification: Notification;
  try {
    notification = new Notification(content.title, {
      body: content.body,
      tag: INCOMING_CALL_TAG,
      requireInteraction: true
    });
  } catch {
    // Some browsers allow notifications only from a service worker.
    return () => {};
  }
  notification.onclick = () => {
    window.focus();
    notification.close();
  };
  return () => notification.close();
}
