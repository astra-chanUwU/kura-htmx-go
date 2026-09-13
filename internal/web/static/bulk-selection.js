(()=>{
  const bulkLimit=24;
  const exportLimit=100;
  const exportBytesLimit=512*1024*1024;
  let syncSelection=()=>{};
  const bind=()=>{
    const browse=document.querySelector('#browse');
    if(!browse)return;
    const exportToolbar=browse.querySelector('[data-selection-toolbar]');
    if(!exportToolbar||exportToolbar.dataset.selectionBound==='true')return;
    exportToolbar.dataset.selectionBound='true';
    const boxes=()=>[...browse.querySelectorAll('[data-export-post]')];
    const sync=()=>{
      const selected=boxes().filter(box=>box.checked);
      const bytes=selected.reduce((sum,box)=>sum+(Number.parseInt(box.dataset.exportBytes||'0',10)||0),0);
      const bulkForm=browse.querySelector('[data-bulk-form]');
      if(bulkForm){
        bulkForm.querySelectorAll('[data-bulk-generated]').forEach(input=>input.remove());
        selected.forEach(box=>{
          const input=document.createElement('input');
          input.type='hidden';input.name='post_ids';input.value=box.dataset.bulkPost||box.dataset.exportPost;input.dataset.bulkGenerated='true';
          bulkForm.append(input);
        });
        const preview=bulkForm.querySelector('[data-bulk-preview]');
        if(preview)preview.disabled=selected.length===0||selected.length>bulkLimit;
      }
      const exportForm=exportToolbar.querySelector('[data-export-form]');
      exportForm.querySelectorAll('[data-export-generated]').forEach(input=>input.remove());
      selected.forEach(box=>{
        const input=document.createElement('input');
        input.type='hidden';input.name='post_ids';input.value=box.dataset.exportPost;input.dataset.exportGenerated='true';
        exportForm.append(input);
      });
      exportToolbar.querySelector('[data-selection-count]').textContent=selected.length;
      exportToolbar.querySelector('[data-selection-bytes]').textContent=formatBytes(bytes);
      exportToolbar.querySelectorAll('[data-export-submit]').forEach(button=>{button.disabled=selected.length===0||selected.length>exportLimit||bytes>exportBytesLimit});
    };
    syncSelection=sync;
    exportToolbar.querySelector('[data-selection-select-page]').addEventListener('click',()=>{boxes().forEach(box=>{box.checked=true});sync()});
    exportToolbar.querySelector('[data-selection-clear]').addEventListener('click',()=>{boxes().forEach(box=>{box.checked=false});sync()});
    boxes().forEach(box=>box.addEventListener('change',sync));
    sync();
  };
  const formatBytes=bytes=>{
    if(bytes<1024)return `${bytes} B`;
    if(bytes<1024*1024)return `${(bytes/1024).toFixed(1)} KB`;
    return `${(bytes/(1024*1024)).toFixed(1)} MB`;
  };
  const reset=()=>document.querySelectorAll('#browse [data-export-post]').forEach(box=>{box.checked=false});
  const restore=()=>{reset();bind();syncSelection()};
       bind();
       document.addEventListener('htmx:afterSwap',bind);
       document.addEventListener('htmx:beforeHistorySave',()=>{reset();syncSelection()});
       document.addEventListener('htmx:historyRestore',restore);
       document.addEventListener('htmx:restored',restore);
       window.addEventListener('popstate',()=>{setTimeout(restore,0)});
       window.addEventListener('pageshow',()=>{setTimeout(restore,0)});
     })();
