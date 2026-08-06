/* GoDex Web Push service worker: displays notifications received from the
 * center's /push endpoint. The center relays live events only — no durable
 * push history — so this worker is intentionally minimal. */
self.addEventListener("push", (event) => {
  let title = "GoDex";
  let body = "";
  try {
    const payload = event.data ? event.data.json() : null;
    if (payload && payload.title) title = payload.title;
    if (payload && payload.body) body = payload.body;
  } catch (_err) {
    if (event.data) body = event.data.text();
  }
  event.waitUntil(
    self.registration.showNotification(title, {
      body,
      icon: "/brand/godex-icon.jpg",
      badge: "/brand/godex-icon.jpg",
      tag: "godex-push",
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clientList) => {
      for (const client of clientList) {
        if ("focus" in client) return client.focus();
      }
      return self.clients.openWindow("/");
    }),
  );
});
