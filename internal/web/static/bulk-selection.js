(()=>{
  const bulkLimit=24;
  const exportLimit=100;
  const exportBytesLimit=512*1024*1024;
  let syncSelection=()=>{};
  let lastChanged=null;
  const narrowTagLayout=window.matchMedia('(max-width:700px)');
  const syncTagDisclosure=()=>{
    const open=!narrowTagLayout.matches;
    document.querySelectorAll('[data-tag-categories]').forEach(details=>{details.open=open});
  };
  narrowTagLayout.addEventListener('change',syncTagDisclosure);
  const bind=()=>{
    syncTagDisclosure();
    const browse=document.querySelector('#browse');
    if(!browse)return;
    const exportToolbar=browse.querySelector('[data-selection-toolbar]');
    if(!exportToolbar||exportToolbar.dataset.selectionBound==='true')return;
    exportToolbar.dataset.selectionBound='true';
    const boxes=()=>[...browse.querySelectorAll('[data-export-post]')];
    const sync=()=>{
      const selected=boxes().filter(box=>box.checked);
      const bytes=selected.reduce((sum,box)=>sum+(Number.parseInt(box.dataset.exportBytes||'0',10)||0),0);
      boxes().forEach(box=>{
        const card=box.closest('.bulk-card');
        if(card)card.dataset.selected=box.checked?'true':'false';
      });
      const selectionActions=exportToolbar.querySelector('[data-selection-actions]');
      if(selectionActions)selectionActions.hidden=selected.length===0;
      const bulkEditor=browse.querySelector('[data-bulk-editor]');
      if(bulkEditor){
        bulkEditor.hidden=selected.length===0;
        if(selected.length===0)bulkEditor.open=false;
      }
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
    exportToolbar.querySelector('[data-selection-select-page]').addEventListener('click',()=>{lastChanged=null;boxes().forEach(box=>{box.checked=true});sync()});
    exportToolbar.querySelector('[data-selection-clear]').addEventListener('click',()=>{lastChanged=null;boxes().forEach(box=>{box.checked=false});sync()});
    boxes().forEach(box=>{
      box.addEventListener('click',event=>{
        if(!event.shiftKey||!lastChanged)return;
        const visibleBoxes=boxes();
        const start=visibleBoxes.indexOf(lastChanged);
        const end=visibleBoxes.indexOf(box);
        if(start<0||end<0)return;
        visibleBoxes.slice(Math.min(start,end),Math.max(start,end)+1).forEach(rangeBox=>{rangeBox.checked=box.checked});
      });
      box.addEventListener('change',event=>{lastChanged=event.currentTarget;sync()});
    });
    sync();
  };
  const formatBytes=bytes=>{
    if(bytes<1024)return `${bytes} B`;
    if(bytes<1024*1024)return `${(bytes/1024).toFixed(1)} KB`;
    return `${(bytes/(1024*1024)).toFixed(1)} MB`;
  };
  const reset=()=>{lastChanged=null;document.querySelectorAll('#browse [data-export-post]').forEach(box=>{box.checked=false})};
  const restore=()=>{reset();bind();syncSelection()};
       bind();
       document.addEventListener('htmx:afterSwap',bind);
       document.addEventListener('htmx:beforeHistorySave',()=>{reset();syncSelection()});
       document.addEventListener('htmx:historyRestore',restore);
       document.addEventListener('htmx:restored',restore);
       window.addEventListener('popstate',()=>{setTimeout(restore,0)});
       window.addEventListener('pageshow',()=>{setTimeout(restore,0)});
     })();
