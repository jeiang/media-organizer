// Morphs the clicked poster into the entry page's poster across the navigation.
// Loaded render-blocking so the listeners exist before the first reveal.

const isEntry = (url) => /^\/library\/(movie|show)\//.test(new URL(url, location.href).pathname);

function cardPoster(url) {
  const path = new URL(url, location.href).pathname;
  for (const link of document.querySelectorAll(".poster-card .card-link")) {
    if (new URL(link.href).pathname === path) return link.closest(".poster-card").querySelector("img");
  }
  return null;
}

function clearNames() {
  for (const img of document.querySelectorAll(".poster-card img")) img.style.viewTransitionName = "";
}

async function tag(img, transition) {
  if (!img) return;
  img.style.viewTransitionName = "poster";
  try {
    await transition.finished;
  } catch {
    // A skipped transition rejects; the name is still cleared below.
  } finally {
    img.style.viewTransitionName = "";
  }
}

addEventListener("pageswap", (e) => {
  if (!e.viewTransition || !e.activation?.entry) return;
  const to = e.activation.entry.url;
  if (isEntry(to) && !isEntry(location.href)) tag(cardPoster(to), e.viewTransition);
});

addEventListener("pagereveal", (e) => {
  clearNames();
  if (!e.viewTransition) return;
  const from = window.navigation?.activation?.from?.url;
  if (from && isEntry(from) && !isEntry(location.href)) tag(cardPoster(from), e.viewTransition);
});
