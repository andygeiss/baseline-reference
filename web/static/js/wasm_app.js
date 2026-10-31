// The loader for the room map's module, and the only script beside htmx and
// wasm_exec.js (patterns/wasm-client.md). It finds the element whose
// data-module names a module, starts that module, and decides nothing: the
// module draws, listens and talks. No build step, no npm.
(async () => {
  const el = document.querySelector("[data-module]");
  if (!el) return;
  const go = new Go(); // defined by wasm_exec.js, which loads first
  try {
    const { instance } = await WebAssembly.instantiateStreaming(fetch(el.dataset.module), go.importObject);
    go.run(instance);
  } catch (err) {
    console.error("the module did not start:", el.dataset.module, err);
  }
})();
