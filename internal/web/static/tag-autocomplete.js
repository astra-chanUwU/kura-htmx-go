(()=>{
  const bind=()=>document.querySelectorAll('[data-tag-autocomplete]:not([data-tag-autocomplete-bound])').forEach(input=>{
    input.dataset.tagAutocompleteBound='true';
    const list=document.querySelector('#'+input.getAttribute('aria-controls'));
    if(!list)return;
    let requestID=0,controller;
    const options=()=>[...list.querySelectorAll('[role="option"]')];
    const hide=()=>{list.hidden=true;list.replaceChildren()};
    const currentToken=()=>{const match=input.value.match(/(^|\s)(\S*)$/);return match?match[2]:''};
    const choose=button=>{
      const token=button.dataset.tagCategory==='general'?button.dataset.tagName:button.dataset.tagCategory+':'+button.dataset.tagName;
      const match=input.value.match(/(^|\s)(\S*)$/);
      const start=match?input.value.length-match[2].length:input.value.length;
      input.value=input.value.slice(0,start)+token+' ';
      hide();input.focus();input.dispatchEvent(new Event('input',{bubbles:true}));
    };
    const show=()=>{const found=options();list.hidden=found.length===0;found.forEach(button=>button.addEventListener('click',()=>choose(button)))};
    const load=async()=>{
      const token=currentToken();
      if(!token){hide();return}
      if(controller)controller.abort();
      controller=new AbortController();
      const id=++requestID;
      try{
        const response=await fetch('/tags/suggest?q='+encodeURIComponent(input.value),{headers:{Accept:'text/html'},signal:controller.signal});
        if(!response.ok||id!==requestID)return;
        list.innerHTML=await response.text();show();
      }catch(error){if(error.name!=='AbortError')hide()}
    };
    input.addEventListener('input',load);
    input.addEventListener('keydown',event=>{
      const found=options();
      if(event.key==='Escape'){hide();return}
      if(event.key==='ArrowDown'&&found.length){event.preventDefault();found[0].focus()}
    });
    list.addEventListener('keydown',event=>{
      const found=options(),index=found.indexOf(document.activeElement);
      if(event.key==='Escape'){event.preventDefault();hide();input.focus();return}
      if(event.key==='ArrowDown'&&index<found.length-1){event.preventDefault();found[index+1].focus()}
      if(event.key==='ArrowUp'){event.preventDefault();index>0?found[index-1].focus():(hide(),input.focus())}
      if(event.key==='Enter'&&index>=0){event.preventDefault();choose(found[index])}
    });
    document.addEventListener('click',event=>{if(event.target!==input&&!list.contains(event.target))hide()});
  });
  bind();
  document.addEventListener('htmx:afterSwap',bind);
})();
