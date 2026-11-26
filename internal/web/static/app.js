// Progressive enhancements; every page works without this script.

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
  filterForm.querySelector("[data-js-hide]")?.setAttribute("hidden", "");
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
