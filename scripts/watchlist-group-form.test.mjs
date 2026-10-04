import assert from 'node:assert/strict';
import test from 'node:test';
import {bindNewListForm} from '../internal/server/web/new-list-form.mjs';

function fixture(create = async () => {}) {
  const element = () => ({hidden:false, disabled:false, value:'', textContent:'', attributes:{},
    focus(){this.focused=true;}, setAttribute(key,value){this.attributes[key]=value;}});
  const ui = Object.fromEntries(['form','input','error','submit','cancel','toggle'].map(key=>[key,element()]));
  ui.form.hidden=true;
  ui.saved=0;
  bindNewListForm({...ui,create,created:()=>ui.saved++});
  ui.event=()=>({preventDefault(){}});
  return ui;
}

test('opens an in-page input, cancels and supports Escape without browser dialogs', () => {
  const ui=fixture();
  ui.toggle.onclick();
  assert.equal(ui.form.hidden,false);
  assert.equal(ui.toggle.attributes['aria-expanded'],'true');
  assert.equal(ui.input.focused,true);
  ui.input.value='discard this';
  ui.cancel.onclick();
  assert.equal(ui.form.hidden,true);
  assert.equal(ui.saved,0);
  ui.toggle.onclick();
  assert.equal(ui.input.value,'');
  ui.form.onkeydown({key:'Escape',preventDefault(){}});
  assert.equal(ui.form.hidden,true);
  assert.equal(ui.toggle.attributes['aria-expanded'],'false');
});

test('whitespace cannot create an invisible group', async () => {
  const ui=fixture(()=>assert.fail('blank group submitted'));
  ui.toggle.onclick();ui.input.value='   ';
  await ui.form.onsubmit(ui.event());
  assert.equal(ui.error.textContent,'Enter a list name.');
  assert.equal(ui.form.hidden,false);
  assert.equal(ui.saved,0);
});

test('a pending save cannot be submitted twice or dismissed before its result', async () => {
  let finish;const names=[];
  const ui=fixture(name=>{names.push(name);return new Promise(resolve=>{finish=resolve;});});
  ui.toggle.onclick();ui.input.value='  temporary group  ';
  const save=ui.form.onsubmit(ui.event());
  await ui.form.onsubmit(ui.event());ui.cancel.onclick();
  assert.deepEqual(names,['TEMPORARY GROUP']);
  assert.equal(ui.submit.disabled,true);
  assert.equal(ui.form.hidden,false);
  assert.equal(ui.saved,0);
  finish();await save;
  assert.equal(ui.form.hidden,true);
  assert.equal(ui.submit.disabled,false);
  assert.equal(ui.saved,1);
});

test('a rejected save preserves the name, shows its error and permits retry', async () => {
  let attempt=0;
  const ui=fixture(async()=>{if(++attempt===1)throw new Error('Storage unavailable');});
  ui.toggle.onclick();ui.input.value='RETRY GROUP';
  await ui.form.onsubmit(ui.event());
  assert.equal(ui.error.textContent,'Storage unavailable');
  assert.equal(ui.input.value,'RETRY GROUP');
  assert.equal(ui.form.hidden,false);
  assert.equal(ui.input.disabled,false);
  assert.equal(ui.saved,0);
  await ui.form.onsubmit(ui.event());
  assert.equal(ui.error.textContent,'');
  assert.equal(ui.form.hidden,true);
  assert.equal(ui.saved,1);
});
