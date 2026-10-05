import { mount } from 'svelte';
import Root from './Root.svelte';
import '@xyflow/svelte/dist/style.css';
import './style.css';

mount(Root, { target: document.getElementById('app')! });
