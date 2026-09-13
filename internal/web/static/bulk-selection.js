(()=>{
  const limit=24;
  const bind=()=>document.querySelectorAll('[data-bulk-toolbar]:not([data-bulk-bound])').forEach(toolbar=>{
    toolbar.dataset.bulkBound='true';
    const browse=toolbar.closest('#browse')||document;
    const form=toolbar.querySelector('[data-bulk-form]');
    const count=toolbar.querySelector('[data-bulk-count]');
    const preview=toolbar.querySelector('[data-bulk-preview]');
    const boxes=()=>[...browse.querySelectorAll('[data-bulk-post-checkbox]')];
    const sync=()=>{
      const selected=boxes().filter(box=>box.checked);
      form.querySelectorAll('[data-bulk-generated]').forEach(input=>input.remove());
      selected.forEach(box=>{
        const input=document.createElement('input');
        input.type='hidden';input.name='post_ids';input.value=box.dataset.bulkPost||box.value;input.dataset.bulkGenerated='true';
        form.append(input);
      });
      count.textContent=selected.length;
      preview.disabled=selected.length===0||selected.length>limit;
    };
    toolbar.querySelector('[data-bulk-select-page]').addEventListener('click',()=>{
      boxes().forEach((box,index)=>{box.checked=index<limit});
      sync();
    });
    toolbar.querySelector('[data-bulk-clear]').addEventListener('click',()=>{
      boxes().forEach(box=>{box.checked=false});
      sync();
    });
    boxes().forEach(box=>box.addEventListener('change',sync));
    sync();
  });
  const reset=()=>document.querySelectorAll('[data-bulk-toolbar]').forEach(toolbar=>{
    const browse=toolbar.closest('#browse')||document;
    browse.querySelectorAll('[data-bulk-post-checkbox]').forEach(box=>{box.checked=false});
    toolbar.querySelectorAll('[data-bulk-generated]').forEach(input=>input.remove());
    const count=toolbar.querySelector('[data-bulk-count]');
    const preview=toolbar.querySelector('[data-bulk-preview]');
    count.textContent='0';preview.disabled=true;
  });
  bind();
  document.addEventListener('htmx:afterSwap',bind);
  document.addEventListener('htmx:historyRestore',reset);
})();
