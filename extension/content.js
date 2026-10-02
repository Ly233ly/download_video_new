const VIDEO_EXTENSION = /\.(mp4|mov|mkv|webm|avi|m4v|mpeg|mpg|ts|m2ts|wmv)(?:$|[?#])/i;
const DOWNLOAD_WORDS = /(idm|download|下载|下載|保存视频|保存影片)/i;

function downloadAction(target) {
  if (!(target instanceof Element)) return null;
  const element = target.closest("a, button, [role='button'], [download], video") || target;
  const href = element.getAttribute?.("href") || "";
  const downloadName = element.getAttribute?.("download") || "";
  const label = [
    element.textContent || "",
    element.getAttribute?.("title") || "",
    element.getAttribute?.("aria-label") || "",
    element.id || "",
    typeof element.className === "string" ? element.className : ""
  ].join(" ").slice(0, 1000);
  const likely = Boolean(downloadName) || VIDEO_EXTENSION.test(href) || DOWNLOAD_WORDS.test(label);
  if (!likely) return null;
  return { mediaUrl: VIDEO_EXTENSION.test(href) ? new URL(href, location.href).href : "" };
}

let lastCaptureAt = 0;

document.addEventListener("pointerdown", (event) => {
  const action = downloadAction(event.target);
  if (!action) return;
  const now = Date.now();
  if (now - lastCaptureAt < 1500) return;
  lastCaptureAt = now;
  chrome.runtime.sendMessage({
    eagleBridge: "sourceClick",
    event: {
      pageUrl: location.href,
      pageTitle: document.title,
      mediaUrl: action.mediaUrl,
      eventType: "page_download_click",
      capturedAt: now
    }
  }).catch(() => {});
}, true);
