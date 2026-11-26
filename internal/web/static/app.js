// Progressive enhancements; every page works without this script.

// Controls replaced by live behaviour (instant filters, live preview).
for (const el of document.querySelectorAll("[data-js-hide]")) el.hidden = true;

const announcer = document.getElementById("announcer");
const supportsPopover = Object.hasOwn(HTMLElement.prototype, "popover");

function announce(text) {
  if ("ariaNotify" in document) {
    document.ariaNotify(text);
  } else if (announcer) {
    setTimeout(() => { announcer.textContent = text; }, 250);
  }
}

// Toasts: rendered from the flash cookie, persistent until closed.
for (const toast of document.querySelectorAll(".toast")) {
  if (supportsPopover) {
    toast.showPopover();
    toast.addEventListener("toggle", (e) => {
      if (e.newState === "closed" && toast.contains(document.activeElement)) {
        document.getElementById("main")?.focus();
      }
    });
  } else {
    toast.querySelector("button")?.addEventListener("click", () => {
      toast.remove();
      document.getElementById("main")?.focus();
    });
  }
  announce(toast.querySelector("p")?.textContent.trim() ?? "");
}

// Light dismiss for browsers without <dialog closedby>.
if (!("closedBy" in HTMLDialogElement.prototype)) {
  const outside = (e, dialog) => {
    const r = dialog.getBoundingClientRect();
    return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
  };
  for (const dialog of document.querySelectorAll('dialog[closedby="any"]')) {
    let pressedOutside = false;
    dialog.addEventListener("pointerdown", (e) => { pressedOutside = outside(e, dialog); });
    dialog.addEventListener("pointerup", (e) => { if (pressedOutside && outside(e, dialog)) dialog.close(); });
  }
}

// Confirmation for destructive forms (data-confirm).
const confirmDialog = document.getElementById("confirm");
document.addEventListener("submit", (e) => {
  const form = e.target;
  if (!(form instanceof HTMLFormElement) || !form.dataset.confirm || !confirmDialog) return;
  if (form.dataset.confirmed) {
    delete form.dataset.confirmed;
    return;
  }
  e.preventDefault();
  const submitter = e.submitter;
  confirmDialog.querySelector("#confirm-title").textContent = form.dataset.confirm;
  confirmDialog.querySelector("#confirm-detail").textContent = form.dataset.confirmDetail ?? "";
  confirmDialog.querySelector('[value="confirm"]').textContent = form.dataset.confirmLabel ?? "Confirm";
  confirmDialog.returnValue = "";
  confirmDialog.addEventListener("close", () => {
    const choice = confirmDialog.returnValue;
    confirmDialog.returnValue = "";
    if (choice === "confirm") {
      form.dataset.confirmed = "1";
      form.requestSubmit(submitter);
    }
  }, { once: true });
  confirmDialog.showModal();
});

// Player: play links open a modal player instead of navigating to the file.
const player = document.getElementById("player");
if (player) {
  const video = player.querySelector("video");
  document.addEventListener("click", (e) => {
    const link = e.target instanceof Element && e.target.closest("a[data-play]");
    if (!link || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    player.querySelector("#player-title").textContent = link.dataset.title ?? "Now playing";
    video.src = link.href;
    player.showModal();
    video.play().catch(() => {});
  });
  player.addEventListener("close", () => {
    video.pause();
    video.removeAttribute("src");
    video.load();
  });
}

// Library: live search over the rendered cards; kind and sort submit at once.
const filterForm = document.querySelector("[data-filter-form]");
if (filterForm) {
  filterForm.addEventListener("change", (e) => {
    if (e.target.matches('[name="kind"], [name="sort"]')) filterForm.requestSubmit();
  });
  const input = filterForm.querySelector("[data-filter]");
  const items = [...document.querySelectorAll(".poster-grid > li")];
  const count = document.querySelector("[data-count]");
  const empty = document.querySelector("[data-filter-empty]");
  input?.addEventListener("input", () => {
    const q = input.value.trim().toLowerCase();
    let shown = 0;
    for (const li of items) {
      const match = !q || li.dataset.title.includes(q);
      li.hidden = !match;
      if (match) shown++;
    }
    if (count) count.textContent = shown;
    if (empty) empty.hidden = shown > 0 || items.length === 0;
    const url = new URL(location.href);
    if (q) url.searchParams.set("q", input.value.trim());
    else url.searchParams.delete("q");
    history.replaceState(null, "", url);
  });
}

// Review: an edit makes the preview stale, so filing waits for a fresh preview.
const review = document.querySelector("form.review");
if (review) {
  const file = review.querySelector("[data-file]");
  review.addEventListener("input", () => {
    if ("stale" in review.dataset) return;
    review.dataset.stale = "";
    if (file) file.disabled = true;
  });
}

// Copy link: buttons with command="--copy" copy the href (or text) of their commandfor target.
// The app is served over plain HTTP on a private network, where navigator.clipboard is
// unavailable, so the textarea fallback is the usual path.
function legacyCopy(text) {
  const previousFocus = document.activeElement;
  const field = Object.assign(document.createElement("textarea"), { value: text });
  field.setAttribute("readonly", "");
  field.className = "offscreen";
  document.body.append(field);
  field.select();
  try {
    return document.execCommand("copy");
  } finally {
    field.remove();
    previousFocus?.focus({ preventScroll: true });
  }
}

const copyTimers = new WeakMap();
let lastCopyMessage = "";
function reportCopy(button, ok) {
  const tip = button.nextElementSibling;
  clearTimeout(copyTimers.get(button));
  button.removeAttribute("data-copied");
  button.removeAttribute("data-failed");
  let message = ok ? "Link copied" : "Copy failed";
  if (message === lastCopyMessage) message += "\u00A0";
  lastCopyMessage = message;
  tip.textContent = "";
  void button.offsetWidth;
  button.setAttribute(ok ? "data-copied" : "data-failed", "");
  requestAnimationFrame(() => { tip.textContent = message; });
  copyTimers.set(button, setTimeout(() => {
    button.removeAttribute("data-copied");
    button.removeAttribute("data-failed");
    tip.textContent = "";
  }, 2000));
}

function copyFrom(button, target) {
  const text = target instanceof HTMLAnchorElement ? target.href : target.innerText.trim();
  if (!text) return;
  // The legacy path must run synchronously inside the click.
  if (!navigator.clipboard || !window.isSecureContext) {
    reportCopy(button, legacyCopy(text));
    return;
  }
  navigator.clipboard.writeText(text).then(
    () => reportCopy(button, true),
    () => reportCopy(button, legacyCopy(text)),
  );
}

if ("CommandEvent" in window) {
  document.addEventListener("command", (e) => {
    if (e.command === "--copy") copyFrom(e.source, e.target);
  }, true);
} else {
  document.addEventListener("click", (e) => {
    if (e.defaultPrevented) return;
    const button = e.target instanceof Element && e.target.closest('button[command="--copy"]');
    const target = button && document.getElementById(button.getAttribute("commandfor"));
    if (target) copyFrom(button, target);
  });
}

// Live preview: announce the match count after each swap, not the whole list.
document.addEventListener("htmx:afterSettle", (e) => {
  if (e.target.id !== "preview-body") return;
  const status = document.getElementById("preview-status");
  if (status) status.textContent = e.target.dataset.summary ?? "";
});

// Downloads: show the chosen file's name, and keep magnet and file exclusive.
for (const form of document.querySelectorAll("form.add-torrent")) {
  const file = form.querySelector('input[type="file"]');
  const magnet = form.querySelector('input[name="magnet"]');
  const label = form.querySelector("[data-file-name]");
  file.addEventListener("change", () => {
    const name = file.files[0]?.name;
    label.textContent = name ?? "Choose .torrent";
    form.toggleAttribute("data-has-file", Boolean(name));
    if (name) magnet.value = "";
  });
  magnet.addEventListener("input", () => {
    if (magnet.value && file.value) {
      file.value = "";
      label.textContent = "Choose .torrent";
      form.removeAttribute("data-has-file");
    }
  });
}
